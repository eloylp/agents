package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/autonomous"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
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

// DispatchStatsProvider reports aggregate dispatch statistics.
// The implementation is optional; passing nil omits the dispatch section.
type DispatchStatsProvider interface {
	DispatchStats() workflow.DispatchStats
}

// AgentTriggerer can run a named autonomous agent on demand.
type AgentTriggerer interface {
	TriggerAgent(ctx context.Context, agentName, repo string) error
}

// RuntimeStateProvider reports whether a named agent currently has an in-flight run.
// The implementation is optional; passing nil causes all agents to report "idle".
type RuntimeStateProvider interface {
	IsRunning(agentName string) bool
}

// EventQueue accepts events for async processing and reports queue depth.
// *workflow.DataChannels satisfies this interface.
type EventQueue interface {
	PushEvent(ctx context.Context, ev workflow.Event) error
	QueueStats() workflow.QueueStat
}

type Server struct {
	cfg           *config.Config
	delivery      *DeliveryStore
	logger        zerolog.Logger
	channels      EventQueue
	provider      StatusProvider
	runtimeState  RuntimeStateProvider // optional; used by /api/agents for live run status
	dispatchStats DispatchStatsProvider
	startTime     time.Time
	triggerer     AgentTriggerer
	proxy         *anthropicproxy.Handler
	uiFS          fs.FS          // optional; when set, /ui/ serves these static files
	observeStore  *observe.Store // optional; when set, enables observability endpoints
}

// WithUI attaches an fs.FS containing the pre-built static UI assets to the
// server. When set, the daemon serves the files at /ui/. Callers that do not
// need the UI (tests, --run-agent mode) can skip this call.
func (s *Server) WithUI(uiFS fs.FS) {
	s.uiFS = uiFS
}

// WithObserve attaches the observability store. When set, the server registers
// the full suite of /api/events, /api/traces, /api/graph, and /api/memory
// endpoints. Callers that do not need the UI can skip this call.
func (s *Server) WithObserve(store *observe.Store) {
	s.observeStore = store
}

// WithRuntimeState attaches an optional runtime-state provider used by
// /api/agents to report which agents are currently running.
func (s *Server) WithRuntimeState(rsp RuntimeStateProvider) {
	s.runtimeState = rsp
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, channels EventQueue, provider StatusProvider, dispatchStats DispatchStatsProvider, logger zerolog.Logger, triggerer AgentTriggerer) *Server {
	s := &Server{
		cfg:           cfg,
		delivery:      delivery,
		logger:        logger.With().Str("component", "webhook_server").Logger(),
		channels:      channels,
		provider:      provider,
		dispatchStats: dispatchStats,
		startTime:     time.Now(),
		triggerer:     triggerer,
	}
	if cfg.Daemon.Proxy.Enabled {
		up := cfg.Daemon.Proxy.Upstream
		s.proxy = anthropicproxy.NewHandler(anthropicproxy.UpstreamConfig{
			URL:       up.URL,
			Model:     up.Model,
			APIKey:    up.APIKey,
			Timeout:   time.Duration(up.TimeoutSeconds) * time.Second,
			ExtraBody: up.ExtraBody,
		}, logger)
	}
	return s
}

// buildHandler constructs the HTTP router for the server and returns it as
// an http.Handler. It is separated from Run so tests can exercise routing
// without starting a real TCP listener.
func (s *Server) buildHandler() http.Handler {
	router := mux.NewRouter()
	router.HandleFunc(s.cfg.Daemon.HTTP.StatusPath, s.handleStatus).Methods(http.MethodGet)
	router.HandleFunc(s.cfg.Daemon.HTTP.WebhookPath, s.handleGitHubWebhook).Methods(http.MethodPost)
	router.Handle(s.cfg.Daemon.HTTP.AgentsRunPath, s.requireAPIKey(http.HandlerFunc(s.handleAgentsRun))).Methods(http.MethodPost)

	// Observability API — always open. Auth is the reverse proxy's responsibility
	// per issue #151 non-goals. The mutation endpoint (/agents/run) remains
	// protected by requireAPIKey; these read-only surfaces do not.
	router.HandleFunc("/api/agents", s.handleAPIAgents).Methods(http.MethodGet)
	router.HandleFunc("/api/config", s.handleAPIConfig).Methods(http.MethodGet)
	router.HandleFunc("/api/dispatches", s.handleAPIDispatches).Methods(http.MethodGet)

	// Extended observability endpoints — only registered when an observe.Store
	// has been attached via WithObserve.
	if s.observeStore != nil {
		router.HandleFunc("/api/events", s.handleAPIEvents).Methods(http.MethodGet)
		router.HandleFunc("/api/events/stream", s.handleAPIEventsStream)
		router.HandleFunc("/api/traces", s.handleAPITraces).Methods(http.MethodGet)
		router.HandleFunc("/api/traces/stream", s.handleAPITracesStream)
		router.Handle("/api/traces/{root_event_id}", http.HandlerFunc(s.handleAPITrace)).Methods(http.MethodGet)
		router.HandleFunc("/api/graph", s.handleAPIGraph).Methods(http.MethodGet)
		router.Handle("/api/memory/{agent}/{repo}", http.HandlerFunc(s.handleAPIMemory)).Methods(http.MethodGet)
		router.HandleFunc("/api/memory/stream", s.handleAPIMemoryStream)
	}

	// Static UI: served from the embedded dist/ tree when a UI FS is provided.
	// Auth is the reverse proxy's responsibility (per issue #151 non-goals).
	if s.uiFS != nil {
		sub, err := fs.Sub(s.uiFS, "dist")
		if err == nil {
			fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
			router.PathPrefix("/ui/").Handler(fileServer)
			// Redirect the slashless entrypoint /ui → /ui/ so operators and
			// reverse proxies that normalise trailing slashes get the dashboard.
			router.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			}).Methods(http.MethodGet)
		}
	}

	if s.proxy != nil {
		router.Handle(s.cfg.Daemon.Proxy.Path, s.proxy).Methods(http.MethodPost)
		router.HandleFunc("/v1/models", s.proxy.ModelsHandler).Methods(http.MethodGet)
		s.logger.Info().Str("path", s.cfg.Daemon.Proxy.Path).Str("upstream", s.cfg.Daemon.Proxy.Upstream.URL).Msg("anthropic proxy enabled")
	}
	return router
}

func (s *Server) Run(ctx context.Context) error {
	router := s.buildHandler()

	srv := &http.Server{
		Addr:         s.cfg.Daemon.HTTP.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(s.cfg.Daemon.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(s.cfg.Daemon.HTTP.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(s.cfg.Daemon.HTTP.IdleTimeoutSeconds) * time.Second,
	}

	// A background goroutine watches for ctx cancellation and triggers HTTP
	// graceful shutdown. ListenAndServe returns ErrServerClosed once Shutdown
	// completes, at which point we return the Shutdown error from errCh.
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.Daemon.HTTP.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	logEvent := s.logger.Info().Str("addr", s.cfg.Daemon.HTTP.ListenAddr).Str("status_path", s.cfg.Daemon.HTTP.StatusPath).Str("webhook_path", s.cfg.Daemon.HTTP.WebhookPath).Str("agents_run_path", s.cfg.Daemon.HTTP.AgentsRunPath)
	if s.proxy != nil {
		logEvent = logEvent.Str("proxy_path", s.cfg.Daemon.Proxy.Path)
	}
	logEvent.Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

// requireAPIKey is HTTP middleware that enforces Bearer-token authentication
// when daemon.http.api_key is configured. When no API key is set the request
// passes through unauthenticated, keeping the observability endpoints open for
// operators that rely solely on network-level access control.
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Daemon.HTTP.APIKey != "" {
			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := authHeader[len(prefix):]
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Daemon.HTTP.APIKey)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	q := s.channels.QueueStats()

	type queueJSON struct {
		Buffered int `json:"buffered"`
		Capacity int `json:"capacity"`
	}
	type statusJSON struct {
		Status        string                  `json:"status"`
		UptimeSeconds int64                   `json:"uptime_seconds"`
		Queues        map[string]queueJSON    `json:"queues"`
		Agents        []AgentStatus           `json:"agents"`
		Dispatch      *workflow.DispatchStats `json:"dispatch,omitempty"`
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
			"events": {Buffered: q.Buffered, Capacity: q.Capacity},
		},
		Agents: agents,
	}
	if s.dispatchStats != nil {
		stats := s.dispatchStats.DispatchStats()
		resp.Dispatch = &stats
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type agentsRunRequest struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

func (s *Server) handleAgentsRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Daemon.HTTP.APIKey == "" {
		http.Error(w, "endpoint disabled: no API key configured", http.StatusForbidden)
		return
	}
	if s.triggerer == nil {
		http.Error(w, "no autonomous agents configured", http.StatusNotImplemented)
		return
	}
	var req agentsRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Repo == "" {
		http.Error(w, "agent and repo fields are required", http.StatusBadRequest)
		return
	}
	if err := s.triggerer.TriggerAgent(r.Context(), req.Agent, req.Repo); err != nil {
		if errors.Is(err, autonomous.ErrDispatchSkipped) {
			s.logger.Info().Str("agent", req.Agent).Str("repo", req.Repo).Msg("on-demand agent run skipped: dispatch already in progress")
			w.WriteHeader(http.StatusOK)
			return
		}
		s.logger.Error().Err(err).Str("agent", req.Agent).Str("repo", req.Repo).Msg("on-demand agent run failed")
		http.Error(w, "agent run failed", http.StatusInternalServerError)
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

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(body, s.cfg.Daemon.HTTP.WebhookSecret, r.Header.Get("X-Hub-Signature-256")) {
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
		s.handleIssuesEvent(r.Context(), w, body, deliveryID)
	case "pull_request":
		s.handlePullRequestEvent(r.Context(), w, body, deliveryID)
	case "issue_comment":
		s.handleIssueCommentEvent(r.Context(), w, body, deliveryID)
	case "pull_request_review":
		s.handlePullRequestReviewEvent(r.Context(), w, body, deliveryID)
	case "pull_request_review_comment":
		s.handlePullRequestReviewCommentEvent(r.Context(), w, body, deliveryID)
	case "push":
		s.handlePushEvent(r.Context(), w, body, deliveryID)
	default:
		s.logger.Warn().Str("event", event).Str("delivery_id", deliveryID).Msg("unhandled webhook event type")
		w.WriteHeader(http.StatusAccepted)
	}
}

// ─── webhook payload shapes ───────────────────────────────────────────────────

type webhookRepository struct {
	FullName string `json:"full_name"`
}

type webhookSender struct {
	Login string `json:"login"`
}

type webhookLabel struct {
	Name string `json:"name"`
}

type webhookIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

type webhookPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
}

type webhookComment struct {
	Body string `json:"body"`
}

type webhookReview struct {
	Body  string `json:"body"`
	State string `json:"state"`
}

// ─── event-type handlers ──────────────────────────────────────────────────────

// handleIssuesEvent handles X-GitHub-Event: issues.
// For "labeled" actions it filters to AI labels and emits "issues.labeled".
// For lifecycle actions (opened, edited, reopened, closed) it emits the
// corresponding "issues.{action}" event.
// Events from issues that are pull requests (GitHub sends both) are dropped
// for the "labeled" action; the pull_request event handles those.
func (s *Server) handleIssuesEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Action     string             `json:"action"`
		Label      webhookLabel       `json:"label"`
		Issue      webhookIssue       `json:"issue"`
		Repository webhookRepository  `json:"repository"`
		Sender     webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repoRef := workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled}

	// GitHub sends issues events for PR-backed issues too; the pull_request event
	// handles those, so drop all issue events that belong to a pull request.
	if payload.Issue.PullRequest != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch payload.Action {
	case "labeled":
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "issues.labeled",
			Number: payload.Issue.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"label": payload.Label.Name,
			},
		}
		s.enqueue(ctx, w, ev, deliveryID)
	case "opened", "edited", "reopened", "closed":
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "issues." + payload.Action,
			Number: payload.Issue.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"title": payload.Issue.Title,
				"body":  payload.Issue.Body,
			},
		}
		s.enqueue(ctx, w, ev, deliveryID)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// handlePullRequestEvent handles X-GitHub-Event: pull_request.
// For "labeled" actions it filters to AI labels (and skips drafts) and emits
// "pull_request.labeled". For lifecycle actions it emits "pull_request.{action}".
func (s *Server) handlePullRequestEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Action      string             `json:"action"`
		Label       webhookLabel       `json:"label"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repoRef := workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled}

	switch payload.Action {
	case "labeled":
		if payload.PullRequest.Draft {
			s.logger.Info().Str("repo", repo.Name).Int("number", payload.PullRequest.Number).Msg("pull request skipped, draft")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		ev := workflow.Event{
			ID:     deliveryID,
			Repo:   repoRef,
			Kind:   "pull_request.labeled",
			Number: payload.PullRequest.Number,
			Actor:  payload.Sender.Login,
			Payload: map[string]any{
				"label": payload.Label.Name,
			},
		}
		s.enqueue(ctx, w, ev, deliveryID)
	case "opened", "synchronize", "ready_for_review", "closed":
		eventPayload := map[string]any{
			"title": payload.PullRequest.Title,
			"draft": payload.PullRequest.Draft,
		}
		if payload.Action == "closed" {
			eventPayload["merged"] = payload.PullRequest.Merged
		}
		ev := workflow.Event{
			ID:      deliveryID,
			Repo:    repoRef,
			Kind:    "pull_request." + payload.Action,
			Number:  payload.PullRequest.Number,
			Actor:   payload.Sender.Login,
			Payload: eventPayload,
		}
		s.enqueue(ctx, w, ev, deliveryID)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// handleIssueCommentEvent handles X-GitHub-Event: issue_comment.
// Only "created" actions are forwarded as "issue_comment.created".
func (s *Server) handleIssueCommentEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Action     string            `json:"action"`
		Comment    webhookComment    `json:"comment"`
		Issue      webhookIssue      `json:"issue"`
		Repository webhookRepository `json:"repository"`
		Sender     webhookSender     `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "issue_comment.created",
		Number: payload.Issue.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"body": payload.Comment.Body,
		},
	}
	s.enqueue(ctx, w, ev, deliveryID)
}

// handlePullRequestReviewEvent handles X-GitHub-Event: pull_request_review.
// Only "submitted" actions are forwarded as "pull_request_review.submitted".
func (s *Server) handlePullRequestReviewEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Action      string             `json:"action"`
		Review      webhookReview      `json:"review"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "submitted" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "pull_request_review.submitted",
		Number: payload.PullRequest.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"state": payload.Review.State,
			"body":  payload.Review.Body,
		},
	}
	s.enqueue(ctx, w, ev, deliveryID)
}

// handlePullRequestReviewCommentEvent handles X-GitHub-Event: pull_request_review_comment.
// Only "created" actions are forwarded as "pull_request_review_comment.created".
func (s *Server) handlePullRequestReviewCommentEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Action      string             `json:"action"`
		Comment     webhookComment     `json:"comment"`
		PullRequest webhookPullRequest `json:"pull_request"`
		Repository  webhookRepository  `json:"repository"`
		Sender      webhookSender      `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "created" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:     deliveryID,
		Repo:   workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:   "pull_request_review_comment.created",
		Number: payload.PullRequest.Number,
		Actor:  payload.Sender.Login,
		Payload: map[string]any{
			"body": payload.Comment.Body,
		},
	}
	s.enqueue(ctx, w, ev, deliveryID)
}

// handlePushEvent handles X-GitHub-Event: push.
func (s *Server) handlePushEvent(ctx context.Context, w http.ResponseWriter, body []byte, deliveryID string) {
	var payload struct {
		Ref        string            `json:"ref"`
		After      string            `json:"after"`
		Repository webhookRepository `json:"repository"`
		Sender     webhookSender     `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo, ok := s.cfg.RepoByName(payload.Repository.FullName)
	if !ok || !repo.Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Ignore branch deletions (After is all-zero SHA) and non-branch refs
	// (tags, notes). Only "new commit pushed to a branch" maps to push events.
	const deletedSHA = "0000000000000000000000000000000000000000"
	if payload.After == deletedSHA || !strings.HasPrefix(payload.Ref, "refs/heads/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	ev := workflow.Event{
		ID:    deliveryID,
		Repo:  workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:  "push",
		Actor: payload.Sender.Login,
		Payload: map[string]any{
			"ref":      payload.Ref,
			"head_sha": payload.After,
		},
	}
	s.enqueue(ctx, w, ev, deliveryID)
}

// enqueue pushes ev onto the event queue, handling all error cases.
func (s *Server) enqueue(ctx context.Context, w http.ResponseWriter, ev workflow.Event, deliveryID string) {
	if err := s.channels.PushEvent(ctx, ev); err != nil {
		if errors.Is(err, workflow.ErrEventQueueFull) {
			s.delivery.Delete(deliveryID)
			s.logger.Warn().Str("repo", ev.Repo.FullName).Str("kind", ev.Kind).Msg("event queue full, dropping webhook")
			http.Error(w, "event queue full, retry later", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, workflow.ErrQueueClosed) {
			s.logger.Warn().Str("repo", ev.Repo.FullName).Msg("queue closed during shutdown, dropping webhook")
			http.Error(w, "shutting down, retry later", http.StatusServiceUnavailable)
			return
		}
		s.delivery.Delete(deliveryID)
		http.Error(w, "request cancelled", http.StatusRequestTimeout)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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
