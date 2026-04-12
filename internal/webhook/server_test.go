package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func TestHandleIssueWebhookDeduplicatesDelivery(t *testing.T) {
	cfg := testCfg(nil)
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"ai:refine"},"repository":{"full_name":"owner/repo"},"issue":{"number":1}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-Hub-Signature-256", sig)

	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first delivery: got %d, want %d", rr.Code, http.StatusAccepted)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req2.Header.Set("X-GitHub-Event", "issues")
	req2.Header.Set("X-GitHub-Delivery", "delivery-1")
	req2.Header.Set("X-Hub-Signature-256", sig)

	rr2 := httptest.NewRecorder()
	server.handleGitHubWebhook(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("dedup delivery: got %d, want %d", rr2.Code, http.StatusAccepted)
	}

	// Only one IssueRequest should have been pushed.
	select {
	case <-dataChannels.IssueChan():
		// ok
	default:
		t.Fatalf("expected first delivery to enqueue an issue")
	}
	select {
	case <-dataChannels.IssueChan():
		t.Fatalf("second duplicate delivery must not enqueue")
	default:
	}
}

func TestHandleWebhookIgnoresNonAILabel(t *testing.T) {
	cfg := testCfg(nil)
	dataChannels := workflow.NewDataChannels(1, 1)
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, nil, zerolog.Nop(), nil)

	body := `{"action":"labeled","label":{"name":"bug"},"repository":{"full_name":"owner/repo"},"pull_request":{"number":2}}`
	sig := signatureForTests([]byte(body), "secret")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-non-ai")
	req.Header.Set("X-Hub-Signature-256", sig)

	rr := httptest.NewRecorder()
	server.handleGitHubWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusAccepted)
	}
	select {
	case <-dataChannels.PRChan():
		t.Fatalf("non-ai label should not enqueue")
	default:
	}
}

func TestInvalidSignatureDoesNotPoisonDeliveryDedupe(t *testing.T) {
	cfg := testCfg(nil)
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

func TestHandleIssueWebhookReturnsServiceUnavailableWhenQueueFull(t *testing.T) {
	cfg := testCfg(nil)
	dataChannels := workflow.NewDataChannels(1, 1)
	// Preload the queue.
	if err := dataChannels.PushIssue(context.Background(), workflow.IssueRequest{
		Repo:  workflow.RepoRef{FullName: cfg.Repos[0].Name, Enabled: cfg.Repos[0].Enabled},
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
		t.Fatalf("queue full: got %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}

	// Delivery ID must be released so a retry can succeed.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req2.Header.Set("X-GitHub-Event", "issues")
	req2.Header.Set("X-GitHub-Delivery", "delivery-queue-full")
	req2.Header.Set("X-Hub-Signature-256", sig)
	// Drain the preloaded item so the next push succeeds.
	<-dataChannels.IssueChan()
	server.handleGitHubWebhook(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("retry: got %d, want %d", rr2.Code, http.StatusAccepted)
	}
}

// --- /agents/run endpoint tests ---

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

func newRunServer(triggerer AgentTriggerer) *Server {
	cfg := testCfg(nil)
	return NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1, 1), nil, zerolog.Nop(), triggerer)
}

func authedRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	return req
}

func TestHandleAgentsRunCallsTriggerer(t *testing.T) {
	trig := &stubTriggerer{}
	server := newRunServer(trig)

	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"coder","repo":"owner/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !trig.called {
		t.Fatal("expected TriggerAgent to be called")
	}
	if trig.agentName != "coder" || trig.repo != "owner/repo" {
		t.Fatalf("unexpected args: agent=%q repo=%q", trig.agentName, trig.repo)
	}
}

func TestHandleAgentsRunRejectsNoAuth(t *testing.T) {
	server := newRunServer(&stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAgentsRunRejectsWrongToken(t *testing.T) {
	server := newRunServer(&stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleAgentsRunReturnsForbiddenWhenNoAPIKeyConfigured(t *testing.T) {
	cfg := testCfg(func(c *config.Config) { c.Daemon.HTTP.APIKey = "" })
	server := NewServer(cfg, NewDeliveryStore(time.Hour), workflow.NewDataChannels(1, 1), nil, zerolog.Nop(), &stubTriggerer{})
	req := httptest.NewRequest(http.MethodPost, "/agents/run", strings.NewReader(`{"agent":"a","repo":"r"}`))
	req.Header.Set("Authorization", "Bearer something")
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestHandleAgentsRunReturnsBadRequestOnMissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing agent", `{"repo":"owner/repo"}`},
		{"missing repo", `{"agent":"coder"}`},
		{"empty body", `{}`},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newRunServer(&stubTriggerer{})
			req := authedRequest(http.MethodPost, "/agents/run", tc.body)
			rr := httptest.NewRecorder()
			server.handleAgentsRun(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want %d", rr.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleAgentsRunReturnsInternalServerErrorOnTriggerFailure(t *testing.T) {
	trig := &stubTriggerer{err: fmt.Errorf("agent not found")}
	server := newRunServer(trig)
	req := authedRequest(http.MethodPost, "/agents/run", `{"agent":"nope","repo":"owner/repo"}`)
	rr := httptest.NewRecorder()
	server.handleAgentsRun(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

// --- signature verification ---

func TestVerifySignature(t *testing.T) {
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

// --- compile-time assertions ---

var _ EventQueue = (*workflow.DataChannels)(nil)
var _ = errors.Is // keep errors import until we add an error-path test
