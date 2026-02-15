package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

type stubWorkflowHandler struct {
	issueCalls  int
	prCalls     int
	issueLabel  string
	prLabel     string
	issueAction string
	prAction    string
}

func (s *stubWorkflowHandler) HandleIssueLabelEvent(_ context.Context, _ config.RepoConfig, _ workflow.Issue, action, label string) (bool, error) {
	s.issueCalls++
	s.issueLabel = label
	s.issueAction = action
	return true, nil
}

func (s *stubWorkflowHandler) HandlePullRequestLabelEvent(_ context.Context, _ config.RepoConfig, _ workflow.PullRequest, action, label string) (bool, error) {
	s.prCalls++
	s.prLabel = label
	s.prAction = action
	return true, nil
}

func TestHandleIssueWebhookDeduplicatesDelivery(t *testing.T) {
	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			MaxBodyBytes:       1024,
			WebhookSecret:      "secret",
			DeliveryTTLSeconds: 3600,
		},
		Repos: []config.RepoConfig{{FullName: "owner/repo", Enabled: true}},
	}
	handler := &stubWorkflowHandler{}
	server := NewServer(cfg, handler, NewDeliveryStore(time.Hour), zerolog.Nop())

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
	if handler.issueCalls != 1 {
		t.Fatalf("expected one issue call, got %d", handler.issueCalls)
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
	if handler.issueCalls != 1 {
		t.Fatalf("expected deduplicated issue calls to stay at 1, got %d", handler.issueCalls)
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
	handler := &stubWorkflowHandler{}
	server := NewServer(cfg, handler, NewDeliveryStore(time.Hour), zerolog.Nop())

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
	if handler.prCalls != 0 {
		t.Fatalf("expected no pr calls, got %d", handler.prCalls)
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
	handler := &stubWorkflowHandler{}
	server := NewServer(cfg, handler, NewDeliveryStore(time.Hour), zerolog.Nop())

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
	if handler.issueLabel != "ai:refine:codex" || handler.issueAction != "labeled" {
		t.Fatalf("expected event label/action to be forwarded, got label=%q action=%q", handler.issueLabel, handler.issueAction)
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
