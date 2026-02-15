package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

type workflowHandler interface {
	HandleIssueLabelEvent(context.Context, config.RepoConfig, workflow.Issue, string, string) (bool, error)
	HandlePullRequestLabelEvent(context.Context, config.RepoConfig, workflow.PullRequest, string, string) (bool, error)
}

type Server struct {
	cfg      *config.Config
	handler  workflowHandler
	delivery *DeliveryStore
	logger   zerolog.Logger
}

func NewServer(cfg *config.Config, handler workflowHandler, delivery *DeliveryStore, logger zerolog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		handler:  handler,
		delivery: delivery,
		logger:   logger.With().Str("component", "webhook_server").Logger(),
	}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.HTTP.StatusPath, s.handleStatus)
	mux.HandleFunc(s.cfg.HTTP.WebhookPath, s.handleGitHubWebhook)

	srv := &http.Server{
		Addr:         s.cfg.HTTP.ListenAddr,
		Handler:      mux,
		ReadTimeout:  time.Duration(s.cfg.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(s.cfg.HTTP.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(s.cfg.HTTP.IdleTimeoutSeconds) * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	s.logger.Info().Str("addr", s.cfg.HTTP.ListenAddr).Str("status_path", s.cfg.HTTP.StatusPath).Str("webhook_path", s.cfg.HTTP.WebhookPath).Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}
	if s.delivery.SeenOrAdd(deliveryID, time.Now()) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(body, s.cfg.HTTP.WebhookSecret, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	switch event {
	case "issues":
		s.handleIssueEvent(w, r, body)
	case "pull_request":
		s.handlePREvent(w, r, body)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

type webhookRepository struct {
	FullName string `json:"full_name"`
}

type issueWebhookPayload struct {
	Action     string            `json:"action"`
	Label      workflow.Label    `json:"label"`
	Repository webhookRepository `json:"repository"`
	Issue      workflow.Issue    `json:"issue"`
}

func (s *Server) handleIssueEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var payload issueWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !isRelevantAction(payload.Action) || !isAILabel(payload.Label.Name) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if payload.Issue.PullRequest != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, err := s.handler.HandleIssueLabelEvent(r.Context(), repo, payload.Issue, payload.Action, payload.Label.Name); err != nil {
		s.logger.Error().Err(err).Str("repo", repo.FullName).Int("issue_number", payload.Issue.Number).Msg("failed to process issue webhook")
		http.Error(w, "failed to process webhook", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type prWebhookPayload struct {
	Action      string               `json:"action"`
	Label       workflow.Label       `json:"label"`
	Repository  webhookRepository    `json:"repository"`
	PullRequest workflow.PullRequest `json:"pull_request"`
}

func (s *Server) handlePREvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var payload prWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !isRelevantAction(payload.Action) || !isAILabel(payload.Label.Name) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if _, err := s.handler.HandlePullRequestLabelEvent(r.Context(), repo, payload.PullRequest, payload.Action, payload.Label.Name); err != nil {
		s.logger.Error().Err(err).Str("repo", repo.FullName).Int("pr_number", payload.PullRequest.Number).Msg("failed to process pr webhook")
		http.Error(w, "failed to process webhook", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func isRelevantAction(action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	return action == "labeled" || action == "unlabeled"
}

func isAILabel(label string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "ai:")
}

func verifySignature(payload []byte, secret, signature string) bool {
	signature = strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	if signature == "" || secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func signatureForTests(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
