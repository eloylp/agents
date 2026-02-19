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
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

const workerShutdownTimeout = 30 * time.Second

type workflowHandler interface {
	HandleIssueLabelEvent(context.Context, workflow.IssueRequest) error
	HandlePullRequestLabelEvent(context.Context, workflow.PRRequest) error
}

type Server struct {
	cfg      *config.Config
	handler  workflowHandler
	delivery *DeliveryStore
	logger   zerolog.Logger

	workersOnce sync.Once
	wg          sync.WaitGroup
	issueQueue  chan workflow.IssueRequest
	prQueue     chan workflow.PRRequest
}

func NewServer(cfg *config.Config, handler workflowHandler, delivery *DeliveryStore, logger zerolog.Logger) *Server {
	return &Server{
		cfg:        cfg,
		handler:    handler,
		delivery:   delivery,
		logger:     logger.With().Str("component", "webhook_server").Logger(),
		issueQueue: make(chan workflow.IssueRequest, cfg.HTTP.IssueQueueBuffer),
		prQueue:    make(chan workflow.PRRequest, cfg.HTTP.PRQueueBuffer),
	}
}

func (s *Server) Run(ctx context.Context) error {
	s.startWorkers(ctx)

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
	err := <-errCh
	waitCh := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitCh)
	}()
	s.logger.Info().Msg("waiting for background workers to finish")
	select {
	case <-waitCh:
		s.logger.Info().Msg("background workers finished, shutdown complete")
	case <-time.After(workerShutdownTimeout):
		s.logger.Warn().Dur("timeout", workerShutdownTimeout).Msg("background workers did not finish before shutdown timeout")
	}
	return err
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

func (s *Server) startWorkers(ctx context.Context) {
	s.workersOnce.Do(func() {
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			for {
				select {
				case <-ctx.Done():
					s.drainIssueQueue()
					return
				case req := <-s.issueQueue:
					if err := s.handler.HandleIssueLabelEvent(ctx, req); err != nil {
						s.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Msg("failed to process issue webhook")
					}
				}
			}
		}()
		go func() {
			defer s.wg.Done()
			for {
				select {
				case <-ctx.Done():
					s.drainPRQueue()
					return
				case req := <-s.prQueue:
					if err := s.handler.HandlePullRequestLabelEvent(ctx, req); err != nil {
						s.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook")
					}
				}
			}
		}()
	})
}

func (s *Server) drainIssueQueue() {
	ctx, cancel := context.WithTimeout(context.Background(), workerShutdownTimeout)
	defer cancel()
	for {
		select {
		case req := <-s.issueQueue:
			if err := s.handler.HandleIssueLabelEvent(ctx, req); err != nil {
				s.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Msg("failed to process issue webhook during shutdown drain")
			}
		default:
			return
		}
	}
}

func (s *Server) drainPRQueue() {
	ctx, cancel := context.WithTimeout(context.Background(), workerShutdownTimeout)
	defer cancel()
	for {
		select {
		case req := <-s.prQueue:
			if err := s.handler.HandlePullRequestLabelEvent(ctx, req); err != nil {
				s.logger.Error().Err(err).Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("failed to process pr webhook during shutdown drain")
			}
		default:
			return
		}
	}
}
