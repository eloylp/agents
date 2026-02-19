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
	cfg        *config.Config
	delivery   *DeliveryStore
	logger     zerolog.Logger
	issueQueue chan<- workflow.IssueRequest
	prQueue    chan<- workflow.PRRequest
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, logger zerolog.Logger, issueQueue chan<- workflow.IssueRequest, prQueue chan<- workflow.PRRequest) *Server {
	return &Server{
		cfg:        cfg,
		delivery:   delivery,
		logger:     logger.With().Str("component", "webhook_server").Logger(),
		issueQueue: issueQueue,
		prQueue:    prQueue,
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
		s.handleIssueEvent(w, body)
	case "pull_request":
		s.handlePREvent(w, body)
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

func (s *Server) handleIssueEvent(w http.ResponseWriter, body []byte) {
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
	s.issueQueue <- workflow.IssueRequest{Repo: repo, Issue: payload.Issue, Action: payload.Action, Label: payload.Label.Name}
	w.WriteHeader(http.StatusAccepted)
}

type prWebhookPayload struct {
	Action      string               `json:"action"`
	Label       workflow.Label       `json:"label"`
	Repository  webhookRepository    `json:"repository"`
	PullRequest workflow.PullRequest `json:"pull_request"`
}

func (s *Server) handlePREvent(w http.ResponseWriter, body []byte) {
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
	s.prQueue <- workflow.PRRequest{Repo: repo, PR: payload.PullRequest, Action: payload.Action, Label: payload.Label.Name}
	w.WriteHeader(http.StatusAccepted)
}

func isRelevantAction(action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	return action == "labeled"
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
