package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, zerolog.Nop())

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
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, zerolog.Nop())

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
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, zerolog.Nop())

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
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, zerolog.Nop())

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
	server := NewServer(cfg, NewDeliveryStore(time.Hour), dataChannels, zerolog.Nop())

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

func signatureForTests(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
