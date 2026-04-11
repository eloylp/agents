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

// AgentStatus is the runtime state of one autonomous agent as reported by /status.
type AgentStatus struct {
	Name       string     `json:"name"`
	Repo       string     `json:"repo"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	NextRun    time.Time  `json:"next_run"`
	LastStatus string     `json:"last_status,omitempty"`
}

// StatusProvider reports the current scheduling state of autonomous agents.
// The implementation is optional; passing nil results in an empty agents list.
type StatusProvider interface {
	AgentStatuses() []AgentStatus
}

// AgentTriggerer can run a named autonomous agent on demand.
type AgentTriggerer interface {
	TriggerAgent(ctx context.Context, agentName, repo string) error
}

type Server struct {
	cfg       *config.Config
	delivery  *DeliveryStore
	logger    zerolog.Logger
	channels  *workflow.DataChannels
	provider  StatusProvider
	startTime time.Time
	triggerer AgentTriggerer
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, channels *workflow.DataChannels, provider StatusProvider, logger zerolog.Logger, triggerer AgentTriggerer) *Server {
	return &Server{
		cfg:       cfg,
		delivery:  delivery,
		logger:    logger.With().Str("component", "webhook_server").Logger(),
		channels:  channels,
		provider:  provider,
		startTime: time.Now(),
		triggerer: triggerer,
	}
}

func (s *Server) Run(ctx context.Context) error {
	router := mux.NewRouter()
	router.HandleFunc(s.cfg.HTTP.StatusPath, s.handleStatus).Methods(http.MethodGet)
	router.HandleFunc(s.cfg.HTTP.WebhookPath, s.handleGitHubWebhook).Methods(http.MethodPost)
	router.HandleFunc(s.cfg.HTTP.AgentsRunPath, s.handleAgentsRun).Methods(http.MethodPost)

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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.HTTP.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	s.logger.Info().Str("addr", s.cfg.HTTP.ListenAddr).Str("status_path", s.cfg.HTTP.StatusPath).Str("webhook_path", s.cfg.HTTP.WebhookPath).Str("agents_run_path", s.cfg.HTTP.AgentsRunPath).Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	issueQ, prQ := s.channels.QueueStats()

	type queueJSON struct {
		Buffered int `json:"buffered"`
		Capacity int `json:"capacity"`
	}
	type statusJSON struct {
		Status        string               `json:"status"`
		UptimeSeconds int64                `json:"uptime_seconds"`
		Queues        map[string]queueJSON `json:"queues"`
		Agents        []AgentStatus        `json:"agents"`
	}

	agents := []AgentStatus{}
	if s.provider != nil {
		if got := s.provider.AgentStatuses(); len(got) > 0 {
			agents = got
		}
	}

	resp := statusJSON{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		Queues: map[string]queueJSON{
			"issues": {Buffered: issueQ.Buffered, Capacity: issueQ.Capacity},
			"prs":    {Buffered: prQ.Buffered, Capacity: prQ.Capacity},
		},
		Agents: agents,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type agentsRunRequest struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

func (s *Server) handleAgentsRun(w http.ResponseWriter, r *http.Request) {
	if s.triggerer == nil {
		http.Error(w, "no autonomous agents configured", http.StatusNotImplemented)
		return
	}
	var req agentsRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Repo == "" {
		http.Error(w, "agent and repo fields are required", http.StatusBadRequest)
		return
	}
	if err := s.triggerer.TriggerAgent(r.Context(), req.Agent, req.Repo); err != nil {
		s.logger.Error().Err(err).Str("agent", req.Agent).Str("repo", req.Repo).Msg("on-demand agent run failed")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
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
	// Delivery dedup is checked only after signature verification so
	// unauthenticated requests cannot poison the dedupe cache.
	if s.delivery.SeenOrAdd(deliveryID, time.Now()) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	switch event {
	case "issues":
		s.handleIssueEvent(r.Context(), w, body, deliveryID)
	case "pull_request":
		s.handlePREvent(r.Context(), w, body, deliveryID)
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

func (s *Server) handleIssueEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
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
			s.delivery.Delete(deliveryID)
			s.logger.Warn().Str("repo", repo.FullName).Msg("issue queue full, dropping webhook")
			http.Error(w, "issue queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, workflow.ErrQueueClosed) {
			s.logger.Warn().Str("repo", repo.FullName).Msg("queue closed during shutdown, dropping webhook")
			http.Error(w, "shutting down, retry later", http.StatusServiceUnavailable)
			return
		}
		s.delivery.Delete(deliveryID)
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

func (s *Server) handlePREvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
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
			s.delivery.Delete(deliveryID)
			s.logger.Warn().Str("repo", repo.FullName).Msg("pr queue full, dropping webhook")
			http.Error(w, "pr queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, workflow.ErrQueueClosed) {
			s.logger.Warn().Str("repo", repo.FullName).Msg("queue closed during shutdown, dropping webhook")
			http.Error(w, "shutting down, retry later", http.StatusServiceUnavailable)
			return
		}
		s.delivery.Delete(deliveryID)
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
