package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// testCfg builds a minimal *config.Config suitable for webhook tests.
// Callers can override fields via the mutator.
func testCfg(mutator func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{
				ListenAddr:         ":0",
				WebhookPath:        "/webhooks/github",
				StatusPath:         "/status",
				MaxBodyBytes:       1024,
				WebhookSecret:      "secret",
				DeliveryTTLSeconds: 3600,
			},
		},
		Repos: []config.RepoDef{{Name: "owner/repo", Enabled: true}},
	}
	if mutator != nil {
		mutator(cfg)
	}
	return cfg
}

func signatureForTests(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newTestServer(cfg *config.Config) (*Server, *workflow.DataChannels) {
	dc := workflow.NewDataChannels(1)
	return NewServer(cfg, NewDeliveryStore(time.Hour), dc, nil, nil, zerolog.Nop()), dc
}

// webhookRequest builds a signed POST request to /webhooks/github.
func webhookRequest(t *testing.T, event, deliveryID, body string) *http.Request {
	t.Helper()
	sig := signatureForTests([]byte(body), "secret")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sig)
	return req
}

// drainEvent returns the next Event from dc or fails the test.
func drainEvent(t *testing.T, dc *workflow.DataChannels) workflow.Event {
	t.Helper()
	select {
	case ev := <-dc.EventChan():
		return ev
	default:
		t.Fatal("expected an event in the queue but found none")
		panic("unreachable")
	}
}

// assertNoEvent fails if dc has a queued event.
func assertNoEvent(t *testing.T, dc *workflow.DataChannels) {
	t.Helper()
	select {
	case <-dc.EventChan():
		t.Fatal("expected no event in the queue but found one")
	default:
	}
}

// ─── issues events ────────────────────────────────────────────────────────────

func TestHandleIssueWebhookDeduplicatesDelivery(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":1},"sender":{"login":"octocat"}}`

	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "delivery-1", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first delivery: got %d, want %d", rr.Code, http.StatusAccepted)
	}

	rr2 := httptest.NewRecorder()
	server.handleGitHubWebhook(rr2, webhookRequest(t, "issues", "delivery-1", body))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("dedup delivery: got %d, want %d", rr2.Code, http.StatusAccepted)
	}

	// Only one Event should have been pushed.
	drainEvent(t, dc)
	assertNoEvent(t, dc)
}

func TestHandleIssuesLabeledEnqueuesEventWithLabel(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":7},"sender":{"login":"octocat"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "d-1", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "issues.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issues.labeled")
	}
	if ev.Number != 7 {
		t.Errorf("number: got %d, want 7", ev.Number)
	}
	if ev.Actor != "octocat" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "octocat")
	}
	if ev.Payload["label"] != "ai:refine" {
		t.Errorf("payload label: got %v, want %q", ev.Payload["label"], "ai:refine")
	}
}

func TestHandleIssuesOpenedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"opened","repository":{"full_name":"owner/repo"},"issue":{"number":10,"title":"Bug","body":"desc"},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "d-opened", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "issues.opened" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issues.opened")
	}
	if ev.Number != 10 {
		t.Errorf("number: got %d, want 10", ev.Number)
	}
	if ev.Actor != "dev" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "dev")
	}
}

// TestHandleNonAILabelEnqueues verifies that non-AI labels are enqueued for both
// pull_request.labeled and issues.labeled so that event-based bindings
// (events: ["pull_request.labeled"] / events: ["issues.labeled"]) can match
// them. Label-based bindings are filtered later by the engine via agentsForEvent.
func TestHandleNonAILabelEnqueues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		event     string
		delivery  string
		body      string
		wantKind  string
		wantLabel string
	}{
		{
			name:      "pull_request.labeled",
			event:     "pull_request",
			delivery:  "delivery-non-ai",
			body:      `{"action":"labeled","label":{"name":"bug"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":2},"sender":{"login":"dev"}}`,
			wantKind:  "pull_request.labeled",
			wantLabel: "bug",
		},
		{
			name:      "issues.labeled",
			event:     "issues",
			delivery:  "delivery-issue-non-ai",
			body:      `{"action":"labeled","label":{"name":"enhancement"},"repository":{"full_name":"owner/repo"},"issue":{"number":9},"sender":{"login":"dev"}}`,
			wantKind:  "issues.labeled",
			wantLabel: "enhancement",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, tc.event, tc.delivery, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			ev := drainEvent(t, dc)
			if ev.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.Payload["label"] != tc.wantLabel {
				t.Errorf("payload label: got %v, want %q", ev.Payload["label"], tc.wantLabel)
			}
		})
	}
}

// TestHandleIssuesEventDropsPRBackedIssueActions verifies that issues events
// for PR-backed issues are dropped for every action type. GitHub routes some
// issue events (labeled, opened, …) for pull requests through the issues
// webhook; the server drops them because pull_request events handle those.
func TestHandleIssuesEventDropsPRBackedIssueActions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "labeled",
			body: `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":3,"pull_request":{}}}`,
		},
		{
			name: "opened",
			body: `{"action":"opened","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "edited",
			body: `{"action":"edited","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "reopened",
			body: `{"action":"reopened","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
		{
			name: "closed",
			body: `{"action":"closed","repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","pull_request":{}},"sender":{"login":"dev"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "d-pr-"+tc.name, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("action %q: got %d, want %d", tc.name, rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── pull_request events ──────────────────────────────────────────────────────

func TestHandlePullRequestLabeledEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:review"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":5},"sender":{"login":"bot"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request", "d-pr-labeled", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.labeled")
	}
	if ev.Payload["label"] != "ai:review" {
		t.Errorf("payload label: got %v", ev.Payload["label"])
	}
}

func TestHandleDraftPRWebhookDoesNotEnqueue(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:review"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":5,"draft":true}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request", "delivery-draft", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

func TestHandlePullRequestOpenedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"opened","repository":{"full_name":"owner/repo"},"pull_request":{"number":8,"title":"feat","draft":false},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request", "d-pr-opened", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request.opened" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.opened")
	}
	if ev.Number != 8 {
		t.Errorf("number: got %d, want 8", ev.Number)
	}
}

func TestHandlePullRequestClosedPayloadIncludesMerged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		merged     bool
		deliveryID string
	}{
		{name: "merged close", merged: true, deliveryID: "d-pr-closed-merged"},
		{name: "non-merged close", merged: false, deliveryID: "d-pr-closed-ordinary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))

			mergedVal := "false"
			if tc.merged {
				mergedVal = "true"
			}
			body := `{"action":"closed","repository":{"full_name":"owner/repo"},"pull_request":{"number":12,"title":"feat","draft":false,"merged":` + mergedVal + `},"sender":{"login":"dev"}}`
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request", tc.deliveryID, body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}

			ev := drainEvent(t, dc)
			if ev.Kind != "pull_request.closed" {
				t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.closed")
			}
			if ev.Number != 12 {
				t.Errorf("number: got %d, want 12", ev.Number)
			}
			got, ok := ev.Payload["merged"].(bool)
			if !ok {
				t.Fatalf("payload[merged] missing or not bool: %v", ev.Payload["merged"])
			}
			if got != tc.merged {
				t.Errorf("payload[merged]: got %v, want %v", got, tc.merged)
			}
		})
	}
}

// ─── comment and review enqueue events ───────────────────────────────────────

func TestHandleCommentAndReviewEventsEnqueue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		eventType   string
		deliveryID  string
		body        string
		wantKind    string
		wantNumber  int
		wantActor   string
		wantPayload map[string]any
	}{
		{
			name:        "issue_comment.created",
			eventType:   "issue_comment",
			deliveryID:  "d-comment",
			body:        `{"action":"created","comment":{"body":"LGTM"},"issue":{"number":11},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`,
			wantKind:    "issue_comment.created",
			wantNumber:  11,
			wantActor:   "reviewer",
			wantPayload: map[string]any{"body": "LGTM"},
		},
		{
			name:        "pull_request_review.submitted",
			eventType:   "pull_request_review",
			deliveryID:  "d-review",
			body:        `{"action":"submitted","review":{"state":"approved","body":"LGTM"},"pull_request":{"number":9},"repository":{"full_name":"owner/repo"},"sender":{"login":"approver"}}`,
			wantKind:    "pull_request_review.submitted",
			wantNumber:  9,
			wantPayload: map[string]any{"state": "approved"},
		},
		{
			name:        "pull_request_review_comment.created",
			eventType:   "pull_request_review_comment",
			deliveryID:  "d-rc-1",
			body:        `{"action":"created","comment":{"body":"nit: rename this"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`,
			wantKind:    "pull_request_review_comment.created",
			wantNumber:  7,
			wantActor:   "reviewer",
			wantPayload: map[string]any{"body": "nit: rename this"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, tc.eventType, tc.deliveryID, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			ev := drainEvent(t, dc)
			if ev.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", ev.Kind, tc.wantKind)
			}
			if ev.Number != tc.wantNumber {
				t.Errorf("number: got %d, want %d", ev.Number, tc.wantNumber)
			}
			if tc.wantActor != "" && ev.Actor != tc.wantActor {
				t.Errorf("actor: got %q, want %q", ev.Actor, tc.wantActor)
			}
			for k, want := range tc.wantPayload {
				if got := ev.Payload[k]; got != want {
					t.Errorf("payload[%q]: got %v, want %v", k, got, want)
				}
			}
		})
	}
}

// TestHandleNonTriggeringActionsIgnored verifies that non-triggering actions for
// comment and review event types are accepted but produce no workflow event.
func TestHandleNonTriggeringActionsIgnored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		eventType string
		deliveryID string
		body      string
	}{
		{
			name:       "issue_comment non-created action ignored",
			eventType:  "issue_comment",
			deliveryID: "d-edit",
			body:       `{"action":"edited","comment":{"body":"updated"},"issue":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
		{
			name:       "pull_request_review non-submitted action ignored",
			eventType:  "pull_request_review",
			deliveryID: "d-dismissed",
			body:       `{"action":"dismissed","review":{"state":"dismissed"},"pull_request":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
		{
			name:       "pull_request_review_comment non-created action ignored",
			eventType:  "pull_request_review_comment",
			deliveryID: "d-rc-2",
			body:       `{"action":"edited","comment":{"body":"updated"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, tc.eventType, tc.deliveryID, tc.body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── push events ─────────────────────────────────────────────────────────────

func TestHandlePushEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"owner/repo"},"sender":{"login":"pusher"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "push", "d-push", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "push" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "push")
	}
	if ev.Actor != "pusher" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "pusher")
	}
	if ev.Payload["ref"] != "refs/heads/main" {
		t.Errorf("payload ref: got %v", ev.Payload["ref"])
	}
	if ev.Payload["head_sha"] != "abc123" {
		t.Errorf("payload head_sha: got %v", ev.Payload["head_sha"])
	}
}

func TestHandlePushIgnoresNonBranchRefs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  string
		sha  string
	}{
		{name: "tag push", ref: "refs/tags/v1.0.0", sha: "abc123"},
		{name: "branch deletion", ref: "refs/heads/main", sha: "0000000000000000000000000000000000000000"},
		{name: "notes ref", ref: "refs/notes/commits", sha: "abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, dc := newTestServer(testCfg(nil))
			body := `{"ref":"` + tc.ref + `","after":"` + tc.sha + `","repository":{"full_name":"owner/repo"},"sender":{"login":"pusher"}}`
			rr := httptest.NewRecorder()
			server.handleGitHubWebhook(rr, webhookRequest(t, "push", "d-push-"+tc.name, body))
			if rr.Code != http.StatusAccepted {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
			}
			assertNoEvent(t, dc)
		})
	}
}

// ─── unknown event ────────────────────────────────────────────────────────────

func TestHandleUnknownEventReturnsAccepted(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"something"}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "unknown_event", "d-unknown", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

// ─── queue-full ───────────────────────────────────────────────────────────────

func TestHandleWebhookReturnsServiceUnavailableWhenQueueFull(t *testing.T) {
	t.Parallel()
	cfg := testCfg(nil)
	dc := workflow.NewDataChannels(1)
	// Preload the queue.
	if err := dc.PushEvent(context.Background(), workflow.Event{
		Repo:   workflow.RepoRef{FullName: cfg.Repos[0].Name, Enabled: cfg.Repos[0].Enabled},
		Kind:   "issues.labeled",
		Number: 99,
	}); err != nil {
		t.Fatalf("preload event queue: %v", err)
	}
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dc, nil, nil, zerolog.Nop())

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":2}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "delivery-queue-full", body))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue full: got %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}

	// Delivery ID must be released so a retry can succeed.
	<-dc.EventChan()
	rr2 := httptest.NewRecorder()
	server.handleGitHubWebhook(rr2, webhookRequest(t, "issues", "delivery-queue-full", body))
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("retry: got %d, want %d", rr2.Code, http.StatusAccepted)
	}
}

// ─── /agents/run endpoint tests ───────────────────────────────────────────────

func newRunServer() *Server {
	cfg := testCfg(nil)
	return NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(10), nil, nil, zerolog.Nop())
}

func newRequest(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}

func TestHandleAgentsRunEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server := newRunServer()

	req := newRequest(http.MethodPost, "/run", `{"agent":"coder","repo":"owner/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "queued" || resp["agent"] != "coder" || resp["repo"] != "owner/repo" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp["event_id"] == "" {
		t.Fatal("expected non-empty event_id")
	}
}


func TestHandleAgentsRunReturnsBadRequestOnMissingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"missing agent", `{"repo":"owner/repo"}`},
		{"missing repo", `{"agent":"coder"}`},
		{"empty body", `{}`},
		{"empty agent", `{"agent":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newRunServer()
			req := newRequest(http.MethodPost, "/run", tc.body)
			rr := httptest.NewRecorder()
			server.handleAgentsRun(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleAgentsRunReturnsNotFoundForUnknownRepo(t *testing.T) {
	t.Parallel()
	server := newRunServer()
	req := newRequest(http.MethodPost, "/run", `{"agent":"coder","repo":"unknown/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// ─── signature verification ───────────────────────────────────────────────────

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"hello":"world"}`)
	secret := "secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySignature(body, secret, sig) {
		t.Fatalf("expected signature to verify")
	}
	if verifySignature(body, secret, "sha256=deadbeef") {
		t.Fatalf("bad signature should not verify")
	}
	if verifySignature(body, "", sig) {
		t.Fatalf("empty secret must not verify")
	}
}

func TestInvalidSignatureDoesNotPoisonDeliveryDedupe(t *testing.T) {
	t.Parallel()
	server, _ := newTestServer(testCfg(nil))

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":7}}`

	reqBad := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqBad.Header.Set("X-GitHub-Event", "issues")
	reqBad.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqBad.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rrBad := httptest.NewRecorder()
	server.handleGitHubWebhook(rrBad, reqBad)
	if rrBad.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: got %d, want %d", rrBad.Code, http.StatusUnauthorized)
	}

	// Retry the same delivery ID with valid sig — it must be processed.
	sig := signatureForTests([]byte(body), "secret")
	reqGood := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqGood.Header.Set("X-GitHub-Event", "issues")
	reqGood.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqGood.Header.Set("X-Hub-Signature-256", sig)
	rrGood := httptest.NewRecorder()
	server.handleGitHubWebhook(rrGood, reqGood)
	if rrGood.Code != http.StatusAccepted {
		t.Fatalf("retry with good sig: got %d body=%s", rrGood.Code, rrGood.Body.String())
	}
}

// TestUISlashlessRedirect verifies that GET /ui (no trailing slash) redirects
// to /ui/ with a 301 when a UI FS is attached to the server. This is the
// canonical entrypoint that operators and reverse proxies tend to use.
func TestUISlashlessRedirect(t *testing.T) {
	t.Parallel()

	// Build a minimal in-memory FS that satisfies fs.Sub("dist").
	uiFS := fstest.MapFS{
		"dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}

	srv, _ := newTestServer(testCfg(nil))
	srv.WithUI(uiFS)

	ts := httptest.NewServer(srv.buildHandler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse // do not follow redirects
		},
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/ui/" {
		t.Fatalf("want Location /ui/, got %q", loc)
	}
}

// TestBuildHandlerObservabilityRoutesAreOpen verifies that the read-only
// observability endpoints and the UI paths are accessible without any
// daemon-level auth. Access control is the reverse proxy's responsibility.
func TestBuildHandlerObservabilityRoutesAreOpen(t *testing.T) {
	t.Parallel()

	uiFS := fstest.MapFS{
		"dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}

	srv, _ := newTestServer(testCfg(nil))
	srv.WithUI(uiFS)
	srv.WithObserve(newTestObserve(t))

	ts := httptest.NewServer(srv.buildHandler())
	t.Cleanup(ts.Close)

	// These read-only routes must NOT require a Bearer token.
	openRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/agents"},
		{http.MethodGet, "/config"},
		{http.MethodGet, "/dispatches"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/traces"},
		{http.MethodGet, "/graph"},
		{http.MethodGet, "/ui/"},
	}

	for _, tc := range openRoutes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("observability route %s %s must be open (no auth required), got 401", tc.method, tc.path)
			}
		})
	}

}

// TestBuildHandlerMCPMountsWhenSet verifies that WithMCP mounts the handler
// at /mcp and is not mounted when WithMCP was never called.
func TestBuildHandlerMCPMountsWhenSet(t *testing.T) {
	t.Parallel()

	t.Run("not mounted by default", func(t *testing.T) {
		t.Parallel()
		srv, _ := newTestServer(testCfg(nil))
		ts := httptest.NewServer(srv.buildHandler())
		t.Cleanup(ts.Close)

		resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("without WithMCP /mcp should 404, got %d", resp.StatusCode)
		}
	})

	t.Run("mounted after WithMCP", func(t *testing.T) {
		t.Parallel()
		srv, _ := newTestServer(testCfg(nil))
		marker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		srv.WithMCP(marker)

		ts := httptest.NewServer(srv.buildHandler())
		t.Cleanup(ts.Close)

		resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTeapot {
			t.Errorf("WithMCP handler should receive /mcp requests, got status %d", resp.StatusCode)
		}
	})
}

// ─── compile-time assertions ──────────────────────────────────────────────────

var _ EventQueue = (*workflow.DataChannels)(nil)
