package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

func TestHandleIssueWebhookDeduplicatesDelivery(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":1,"title":"t","body":"b","updated_at":"2026-02-15T00:00:00Z","labels":[{"name":"ai:refine"}]}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
	select {
	case msg := <-dataChannels.IssueChan():
		if msg.Label != "ai:refine" {
			t.Fatalf("expected ai:refine label enqueued, got %q", msg.Label)
		}
	default:
		t.Fatalf("expected issue message enqueued")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req2.Header.Set("X-GitHub-Event", "issues")
	req2.Header.Set("X-GitHub-Delivery", "delivery-1")
	req2.Header.Set("X-Hub-Signature-256", sig)
	rr2 := httptest.NewRecorder()
	server.handleGitHubWebhook(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("expected duplicate status %d, got %d", http.StatusAccepted, rr2.Code)
	}
	select {
	case <-dataChannels.IssueChan():
		t.Fatalf("expected duplicate delivery to be ignored")
	default:
	}
}

func TestHandleWebhookIgnoresNonAILabel(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"bug"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":2,"title":"t","body":"b","updated_at":"2026-02-15T00:00:00Z","labels":[{"name":"bug"}],"head":{"sha":"abc"}}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-2")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
	select {
	case <-dataChannels.PRChan():
		t.Fatalf("expected no pr messages enqueued")
	default:
	}
}

func TestInvalidSignatureDoesNotPoisonDeliveryDedupe(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":7}}`

	reqBad := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqBad.Header.Set("X-GitHub-Event", "issues")
	reqBad.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqBad.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rrBad := httptest.NewRecorder()
	server.handleGitHubWebhook(rrBad, reqBad)
	if rrBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid signature status %d, got %d", http.StatusUnauthorized, rrBad.Code)
	}

	reqGood := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqGood.Header.Set("X-GitHub-Event", "issues")
	reqGood.Header.Set("X-GitHub-Delivery", "delivery-poison")
	reqGood.Header.Set("X-Hub-Signature-256", signatureForTests([]byte(body), "secret"))
	rrGood := httptest.NewRecorder()
	server.handleGitHubWebhook(rrGood, reqGood)
	if rrGood.Code != http.StatusAccepted {
		t.Fatalf("expected valid retry status %d, got %d", http.StatusAccepted, rrGood.Code)
	}
	select {
	case <-dataChannels.IssueChan():
	default:
		t.Fatalf("expected valid signed delivery to enqueue")
	}
}

func TestHandleIssueWebhookUsesEventLabelAsTrigger(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"ai:refine:codex"},"repository":{"full_name":"owner/repo"},"issue":{"number":3,"title":"t","body":"b","updated_at":"2026-02-15T00:00:00Z","labels":[{"name":"ai:refine:claude"}]}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-3")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
	select {
	case msg := <-dataChannels.IssueChan():
		if msg.Label != "ai:refine:codex" {
			t.Fatalf("expected event label to be forwarded, got label=%q", msg.Label)
		}
	default:
		t.Fatalf("expected issue message enqueued")
	}
}

func TestHandleIssueWebhookReturnsServiceUnavailableWhenQueueFull(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	if err := dataChannels.PushIssue(context.Background(), workflow.IssueRequest{
		Repo:  workflow.RepoRef{FullName: cfg.Repos[0].FullName, Enabled: cfg.Repos[0].Enabled},
		Issue: workflow.Issue{Number: 99},
		Label: "ai:refine",
	}); err != nil {
		t.Fatalf("preload issue queue: %v", err)
	}
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":2}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-queue-full")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}

	// Drain one item to make room, then retry with the same delivery ID.
	select {
	case <-dataChannels.IssueChan():
	default:
		t.Fatalf("expected preloaded queue item")
	}

	reqRetry := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	reqRetry.Header.Set("X-GitHub-Event", "issues")
	reqRetry.Header.Set("X-GitHub-Delivery", "delivery-queue-full")
	reqRetry.Header.Set("X-Hub-Signature-256", sig)
	rrRetry := httptest.NewRecorder()
	server.handleGitHubWebhook(rrRetry, reqRetry)
	if rrRetry.Code != http.StatusAccepted {
		t.Fatalf("expected retry status %d, got %d", http.StatusAccepted, rrRetry.Code)
	}
}

type stubTriggerer struct {
	called    bool
	agentName string
	repo      string
	err       error
}

func (s *stubTriggerer) TriggerAgent(_ context.Context, agentName, repo string) error {
	s.called = true
	s.agentName = agentName
	s.repo = repo
	return s.err
}

const testAPIKey = "test-secret-key"

func newRunServer(triggerer AgentTriggerer) *Server {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:  1024,
			AgentsRunPath: "/agents/run",
			APIKey:        testAPIKey,
		},
	}
	return NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1, 1), nil, zerolog.Nop(), triggerer)
}

func authedRequest(method, path string, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	return req
}

func TestHandleAgentsRunCallsTriggerer(t *testing.T) {
	t.Parallel()
	trig := &stubTriggerer{}
	server := newRunServer(trig)

	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"bug-fixer","repo":"owner/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !trig.called {
		t.Fatal("expected TriggerAgent to be called")
	}
	if trig.agentName != "bug-fixer" || trig.repo != "owner/repo" {
		t.Fatalf("unexpected args: agent=%q repo=%q", trig.agentName, trig.repo)
	}
}

func TestHandleAgentsRunRejectsNoAuth(t *testing.T) {
	t.Parallel()
	server := newRunServer(&stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleAgentsRunRejectsWrongToken(t *testing.T) {
	t.Parallel()
	server := newRunServer(&stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestHandleAgentsRunReturnsForbiddenWhenNoAPIKeyConfigured(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:  1024,
			AgentsRunPath: "/agents/run",
			APIKey:        "", // no key configured
		},
	}
	server := NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1, 1), nil, zerolog.Nop(), &stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer something")
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
	}
}

func TestHandleAgentsRunReturnsBadRequestOnMissingFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{"missing agent", `{"repo":"owner/repo"}`},
		{"missing repo", `{"agent":"bug-fixer"}`},
		{"empty body", `{}`},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newRunServer(&stubTriggerer{})
			req := authedRequest(http.MethodPost, "/agents/run", tc.body)
			rr := httptest.NewRecorder()
			server.handleAgentsRun(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected %d, got %d", http.StatusBadRequest, rr.Code)
			}
		})
	}
}

func TestHandleAgentsRunReturnsNotImplementedWhenNoTriggerer(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:  1024,
			AgentsRunPath: "/agents/run",
			APIKey:        testAPIKey,
		},
	}
	server := NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1, 1), nil, zerolog.Nop(), nil)
	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"a","repo":"r"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected %d, got %d", http.StatusNotImplemented, rr.Code)
	}
}

func TestHandleAgentsRunReturnsInternalServerErrorOnTriggerFailure(t *testing.T) {
	t.Parallel()
	trig := &stubTriggerer{err: errors.New("agent not found")}
	server := newRunServer(trig)
	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"nonexistent","repo":"owner/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected %d, got %d", http.StatusInternalServerError, rr.Code)
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "secret"
	signature := signatureForTests(body, secret)
	if !verifySignature(body, secret, signature) {
		t.Fatalf("expected signature to verify")
	}
	if verifySignature(body, secret, "sha256=deadbeef") {
		t.Fatalf("expected invalid signature to fail")
	}
}

func TestHandleStatusReturnsJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	dataChannels := workflow.NewDataChannels(8, 4)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	server.handleStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var resp struct {
		Status        string `json:"status"`
		UptimeSeconds int64  `json:"uptime_seconds"`
		Queues        struct {
			Issues struct {
				Buffered int `json:"buffered"`
				Capacity int `json:"capacity"`
			} `json:"issues"`
			PRs struct {
				Buffered int `json:"buffered"`
				Capacity int `json:"capacity"`
			} `json:"prs"`
		} `json:"queues"`
		Agents []AgentStatus `json:"agents"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.Queues.Issues.Capacity != 8 {
		t.Errorf("expected issue queue capacity 8, got %d", resp.Queues.Issues.Capacity)
	}
	if resp.Queues.PRs.Capacity != 4 {
		t.Errorf("expected PR queue capacity 4, got %d", resp.Queues.PRs.Capacity)
	}
	if resp.Agents == nil {
		t.Errorf("expected non-nil agents slice")
	}
}

func TestHandleStatusIncludesAgentStatuses(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
	}
	now := time.Now().UTC().Truncate(time.Second)
	provider := &stubStatusProvider{
		statuses: []AgentStatus{
			{Name: "bug-fixer", Repo: "owner/repo", LastRun: &now, NextRun: now.Add(time.Hour), LastStatus: "success"},
		},
	}
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, provider, zerolog.Nop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	server.handleStatus(rr, req)

	var resp struct {
		Agents []AgentStatus `json:"agents"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(resp.Agents))
	}
	if resp.Agents[0].Name != "bug-fixer" {
		t.Errorf("expected agent name bug-fixer, got %q", resp.Agents[0].Name)
	}
	if resp.Agents[0].LastStatus != "success" {
		t.Errorf("expected last_status success, got %q", resp.Agents[0].LastStatus)
	}
	if resp.Agents[0].LastRun == nil {
		t.Errorf("expected last_run to be set")
	}
}

type stubStatusProvider struct {
	statuses []AgentStatus
}

func (s *stubStatusProvider) AgentStatuses() []AgentStatus {
	return s.statuses
}

// stubEventQueue is a minimal EventQueue that records pushes and can be
// configured to return a specific error.
type stubEventQueue struct {
	issuePushed int
	prPushed    int
	err         error
}

func (q *stubEventQueue) PushIssue(_ context.Context, _ workflow.IssueRequest) error {
	q.issuePushed++
	return q.err
}

func (q *stubEventQueue) PushPR(_ context.Context, _ workflow.PRRequest) error {
	q.prPushed++
	return q.err
}

func (q *stubEventQueue) QueueStats() (issues, prs workflow.QueueStat) {
	return workflow.QueueStat{Buffered: q.issuePushed, Capacity: 256},
		workflow.QueueStat{Buffered: q.prPushed, Capacity: 256}
}

func TestServerAcceptsEventQueueInterface(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	queue := &stubEventQueue{}
	server := NewServer(cfg, NewDeliveryStore(time.Hour), queue, nil, zerolog.Nop(), nil)

	body := []byte(`{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":1,"title":"t","body":"b","updated_at":"2026-02-15T00:00:00Z","labels":[{"name":"ai:refine"}]}}`)
	sig := signatureForTests(body, "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-stub-1")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected %d, got %d", http.StatusAccepted, rr.Code)
	}
	if queue.issuePushed != 1 {
		t.Fatalf("expected 1 issue pushed via stub, got %d", queue.issuePushed)
	}
}

func signatureForTests(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
