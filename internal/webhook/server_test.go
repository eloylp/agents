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

const testAPIKey = "test-secret-key"

// testCfg builds a minimal *config.Config suitable for webhook tests.
// Callers can override fields via the mutator.
func testCfg(mutator func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{
				ListenAddr:         ":0",
				WebhookPath:        "/webhooks/github",
				StatusPath:         "/status",
				AgentsRunPath:      "/agents/run",
				MaxBodyBytes:       1024,
				WebhookSecret:      "secret",
				DeliveryTTLSeconds: 3600,
				APIKey:             testAPIKey,
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

func TestHandleWebhookNonAILabelEnqueues(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	// Non-AI labels on pull_request.labeled must be enqueued so that
	// event-based bindings (events: ["pull_request.labeled"]) can match them.
	// Label-based bindings are still filtered by the engine via agentsForEvent.
	body := `{"action":"labeled","label":{"name":"bug"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":2},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request", "delivery-non-ai", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request.labeled")
	}
	if ev.Payload["label"] != "bug" {
		t.Errorf("payload label: got %v", ev.Payload["label"])
	}
}

func TestHandleIssuesLabeledNonAILabelEnqueues(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	// Non-AI labels on issues.labeled must be enqueued so that
	// event-based bindings (events: ["issues.labeled"]) can match them.
	body := `{"action":"labeled","label":{"name":"enhancement"},"repository":{"full_name":"owner/repo"},"issue":{"number":9},"sender":{"login":"dev"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issues", "delivery-issue-non-ai", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	ev := drainEvent(t, dc)
	if ev.Kind != "issues.labeled" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issues.labeled")
	}
	if ev.Payload["label"] != "enhancement" {
		t.Errorf("payload label: got %v", ev.Payload["label"])
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

// ─── issue_comment events ─────────────────────────────────────────────────────

func TestHandleIssueCommentCreatedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"created","comment":{"body":"LGTM"},"issue":{"number":11},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issue_comment", "d-comment", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "issue_comment.created" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "issue_comment.created")
	}
	if ev.Number != 11 {
		t.Errorf("number: got %d, want 11", ev.Number)
	}
	if ev.Actor != "reviewer" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "reviewer")
	}
	if ev.Payload["body"] != "LGTM" {
		t.Errorf("payload body: got %v", ev.Payload["body"])
	}
}

func TestHandleIssueCommentNonCreatedIgnored(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"edited","comment":{"body":"updated"},"issue":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "issue_comment", "d-edit", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

// ─── pull_request_review events ───────────────────────────────────────────────

func TestHandlePullRequestReviewSubmittedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"submitted","review":{"state":"approved","body":"LGTM"},"pull_request":{"number":9},"repository":{"full_name":"owner/repo"},"sender":{"login":"approver"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request_review", "d-review", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request_review.submitted" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request_review.submitted")
	}
	if ev.Number != 9 {
		t.Errorf("number: got %d, want 9", ev.Number)
	}
	if ev.Payload["state"] != "approved" {
		t.Errorf("payload state: got %v", ev.Payload["state"])
	}
}

func TestHandlePullRequestReviewNonSubmittedIgnored(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"dismissed","review":{"state":"dismissed"},"pull_request":{"number":1},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request_review", "d-dismissed", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
}

// ─── pull_request_review_comment events ──────────────────────────────────────

func TestHandlePullRequestReviewCommentCreatedEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"created","comment":{"body":"nit: rename this"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"reviewer"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request_review_comment", "d-rc-1", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}

	ev := drainEvent(t, dc)
	if ev.Kind != "pull_request_review_comment.created" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "pull_request_review_comment.created")
	}
	if ev.Number != 7 {
		t.Errorf("number: got %d, want 7", ev.Number)
	}
	if ev.Actor != "reviewer" {
		t.Errorf("actor: got %q, want %q", ev.Actor, "reviewer")
	}
	if ev.Payload["body"] != "nit: rename this" {
		t.Errorf("payload body: got %v", ev.Payload["body"])
	}
}

func TestHandlePullRequestReviewCommentNonCreatedIgnored(t *testing.T) {
	t.Parallel()
	server, dc := newTestServer(testCfg(nil))

	body := `{"action":"edited","comment":{"body":"updated"},"pull_request":{"number":7},"repository":{"full_name":"owner/repo"},"sender":{"login":"u"}}`
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, webhookRequest(t, "pull_request_review_comment", "d-rc-2", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	assertNoEvent(t, dc)
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

func authedRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	return req
}

func TestHandleAgentsRunEnqueuesEvent(t *testing.T) {
	t.Parallel()
	server := newRunServer()

	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"coder","repo":"owner/repo"}`)
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

func TestHandleAgentsRunRejectsNoAuth(t *testing.T) {
	t.Parallel()
	server := newRunServer()
	handler := server.requireAPIKey(http.HandlerFunc(server.handleAgentsRun))
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAgentsRunRejectsWrongToken(t *testing.T) {
	t.Parallel()
	server := newRunServer()
	handler := server.requireAPIKey(http.HandlerFunc(server.handleAgentsRun))
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAgentsRunBlocksNonBearerScheme(t *testing.T) {
	t.Parallel()
	server := newRunServer()
	handler := server.requireAPIKey(http.HandlerFunc(server.handleAgentsRun))
	tests := []struct {
		name   string
		header string
	}{
		{"raw key", testAPIKey},
		{"Basic scheme", "Basic " + testAPIKey},
		{"Token scheme", "Token " + testAPIKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
			req.Header.Set("Authorization", tc.header)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("scheme %q: got %d, want %d", tc.header, rr.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestHandleAgentsRunReturnsForbiddenWhenNoAPIKeyConfigured(t *testing.T) {
	t.Parallel()
	cfg := testCfg(func(c *config.Config) { c.Daemon.HTTP.APIKey = "" })
	server := NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1), nil, nil, zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer something")
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusForbidden)
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newRunServer()
			req := authedRequest(http.MethodPost, "/agents/run", tc.body)
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
	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"coder","repo":"unknown/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleAgentsRunReturnsBadRequestForMissingFields(t *testing.T) {
	t.Parallel()
	server := newRunServer()
	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":""}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
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
	server, dc := newTestServer(testCfg(nil))

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
	_ = dc
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
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
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
// observability endpoints and the UI paths are accessible without a Bearer
// token even when daemon.http.api_key is configured. The embedded dashboard
// makes same-origin fetch/EventSource calls that cannot attach an Authorization
// header (EventSource has no header API), so daemon-level auth must not be
// applied here — access control is the reverse proxy's responsibility.
// The mutation endpoint /agents/run must still require the Bearer token.
func TestBuildHandlerObservabilityRoutesAreOpen(t *testing.T) {
	t.Parallel()

	uiFS := fstest.MapFS{
		"dist/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}

	srv, _ := newTestServer(testCfg(nil)) // testCfg sets APIKey = testAPIKey
	srv.WithUI(uiFS)
	srv.WithObserve(newTestObserve())

	ts := httptest.NewServer(srv.buildHandler())
	t.Cleanup(ts.Close)

	// These read-only routes must NOT require a Bearer token.
	openRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/agents"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/dispatches"},
		{http.MethodGet, "/api/events"},
		{http.MethodGet, "/api/traces"},
		{http.MethodGet, "/api/graph"},
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

	// The mutation endpoint must still require the Bearer token.
	t.Run("POST /agents/run requires auth", func(t *testing.T) {
		t.Parallel()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/agents/run", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("want 401 for unauthenticated /agents/run, got %d", resp.StatusCode)
		}
	})
}

// ─── compile-time assertions ──────────────────────────────────────────────────

var _ EventQueue = (*workflow.DataChannels)(nil)
