package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

type Server struct {
	cfg      *config.Config
	delivery *DeliveryStore
	logger   zerolog.Logger
	channels *workflow.DataChannels
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, channels *workflow.DataChannels, logger zerolog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		delivery: delivery,
		logger:   logger.With().Str("component", "webhook_server").Logger(),
		channels: channels,
	}
}

func (s *Server) Run(ctx context.Context) error {
	router := mux.NewRouter()
	router.HandleFunc(s.cfg.HTTP.StatusPath, s.handleStatus).Methods(http.MethodGet)
	router.HandleFunc(s.cfg.HTTP.WebhookPath, s.handleGitHubWebhook).Methods(http.MethodPost)

	srv := &http.Server{
		Addr:         s.cfg.HTTP.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(s.cfg.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(s.cfg.HTTP.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(s.cfg.HTTP.IdleTimeoutSeconds) * time.Second,
	}

	// A background goroutine watches for ctx cancellation and triggers HTTP
	// graceful shutdown. ListenAndServe returns ErrServerClosed once Shutdown
	// completes, at which point we return the Shutdown error from errCh.
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
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}
	// Delivery dedup is checked before signature verification to avoid
	// computing the HMAC for replayed deliveries. GitHub retries failed
	// deliveries with the same X-GitHub-Delivery ID, so 202 is returned to
	// suppress retries without reprocessing.
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
		s.handleIssueEvent(r.Context(), w, body)
	case "pull_request":
		s.handlePREvent(r.Context(), w, body)
	default:
		s.logger.Warn().Str("event", event).Str("delivery_id", deliveryID).Msg("unhandled webhook event type")
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

func (s *Server) handleIssueEvent(ctx context.Context, w http.ResponseWriter, body []byte) {
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
	req := workflow.IssueRequest{
		Repo:  workflow.RepoRef{FullName: repo.FullName, Enabled: repo.Enabled},
		Issue: payload.Issue,
		Label: payload.Label.Name,
	}
	if err := s.channels.PushIssue(ctx, req); err != nil {
		if errors.Is(err, workflow.ErrIssueQueueFull) {
			s.logger.Warn().Str("repo", repo.FullName).Msg("issue queue full, dropping webhook")
			http.Error(w, "issue queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
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

func (s *Server) handlePREvent(ctx context.Context, w http.ResponseWriter, body []byte) {
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
	req := workflow.PRRequest{
		Repo:  workflow.RepoRef{FullName: repo.FullName, Enabled: repo.Enabled},
		PR:    payload.PullRequest,
		Label: payload.Label.Name,
	}
	if err := s.channels.PushPR(ctx, req); err != nil {
		if errors.Is(err, workflow.ErrPRQueueFull) {
			s.logger.Warn().Str("repo", repo.FullName).Msg("pr queue full, dropping webhook")
			http.Error(w, "pr queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func isRelevantAction(action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	return action == "labeled"
}

func isAILabel(label string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "ai:")
}

// verifySignature checks the HMAC-SHA256 signature from X-Hub-Signature-256.
// hmac.Equal is used for the final comparison to avoid timing attacks that
// could leak information about the expected value through execution time.
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
