package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
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

// ConfigObserver is notified after every successful reloadConfig call so that
// execution components can pick up the new config without a daemon restart.
// Implementations must be safe for concurrent use.
type ConfigObserver interface {
	UpdateConfig(*config.Config)
}

type Server struct {
	// cfgPtr holds the current effective config as an atomic pointer so that
	// write-API handlers can swap it after persisting a change without holding
	// a coarse lock across all request handlers.
	cfgPtr        atomic.Pointer[config.Config]
	db            *sql.DB          // non-nil only when --db is used; enables write API
	observers     []ConfigObserver // notified after every successful reloadConfig
	delivery      *DeliveryStore
	logger        zerolog.Logger
	channels      EventQueue
	provider      StatusProvider
	runtimeState  RuntimeStateProvider // optional; used by /api/agents for live run status
	dispatchStats DispatchStatsProvider
	startTime     time.Time
	proxy         *anthropicproxy.Handler
	uiFS          fs.FS          // optional; when set, /ui/ serves these static files
	observeStore  *observe.Store // optional; when set, enables observability endpoints

	// testReloadHook is an optional override injected by tests to simulate
	// reloadConfig failures. When non-nil it replaces the real reload logic.
	testReloadHook func() error
}

// cfg returns the current effective configuration. All request handlers must
// call this instead of accessing the underlying pointer directly.
func (s *Server) cfg() *config.Config {
	return s.cfgPtr.Load()
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

// WithStore attaches a SQLite database to the server. When set, the server
// registers CRUD write endpoints under /api/ so the fleet can be managed
// without restarting the daemon.
func (s *Server) WithStore(db *sql.DB) {
	s.db = db
}

// WithConfigObserver registers a component that should receive config updates
// after every successful write-API mutation. Multiple observers are supported;
// each is called synchronously in the order registered.
func (s *Server) WithConfigObserver(o ConfigObserver) {
	s.observers = append(s.observers, o)
}

// reloadConfig reloads the structural config from the SQLite database, applies
// defaults, normalisation, and validation via config.FinishLoad, then
// atomically replaces the server's config pointer. Resolved runtime secrets
// (webhook secret, API key, proxy API key) are not stored in the database —
// they are carried forward from the current live config so they remain valid
// without requiring a re-read of environment variables.
// After the server's pointer is updated every registered ConfigObserver is
// notified so that execution components (Engine, Scheduler) pick up the new
// routing and agent definitions on their next operation.
func (s *Server) reloadConfig() error {
	if s.testReloadHook != nil {
		return s.testReloadHook()
	}
	raw, err := store.Load(s.db)
	if err != nil {
		return err
	}
	// Copy runtime secrets into the freshly-loaded config BEFORE FinishLoad so
	// that resolveSecrets (which only sets a field when it is empty) leaves them
	// intact and validate does not fail on a missing webhook secret.
	live := s.cfgPtr.Load()
	raw.Daemon.HTTP.WebhookSecret = live.Daemon.HTTP.WebhookSecret
	raw.Daemon.HTTP.APIKey = live.Daemon.HTTP.APIKey
	raw.Daemon.Proxy.Upstream.APIKey = live.Daemon.Proxy.Upstream.APIKey
	cfg, err := config.FinishLoad(raw)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	s.cfgPtr.Store(cfg)
	for _, o := range s.observers {
		o.UpdateConfig(cfg)
	}
	return nil
}

func NewServer(initialCfg *config.Config, delivery *DeliveryStore, channels EventQueue, provider StatusProvider, dispatchStats DispatchStatsProvider, logger zerolog.Logger) *Server {
	s := &Server{
		delivery:      delivery,
		logger:        logger.With().Str("component", "webhook_server").Logger(),
		channels:      channels,
		provider:      provider,
		dispatchStats: dispatchStats,
		startTime:     time.Now(),
	}
	s.cfgPtr.Store(initialCfg)
	if initialCfg.Daemon.Proxy.Enabled {
		up := initialCfg.Daemon.Proxy.Upstream
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
	// http.TimeoutHandler bounds handler execution time (i.e. how long the
	// handler function runs before it must start writing). It is NOT a
	// replacement for http.Server.WriteTimeout, which enforces a socket write
	// deadline and is still set in Run(). SSE handlers clear that write
	// deadline for themselves via http.ResponseController.SetWriteDeadline so
	// they can stream indefinitely; see serveSSEWithInterval in api.go.
	writeTimeout := time.Duration(s.cfg().Daemon.HTTP.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}

	router := mux.NewRouter()
	router.Handle(s.cfg().Daemon.HTTP.StatusPath, withTimeout(http.HandlerFunc(s.handleStatus))).Methods(http.MethodGet)
	router.Handle(s.cfg().Daemon.HTTP.WebhookPath, withTimeout(http.HandlerFunc(s.handleGitHubWebhook))).Methods(http.MethodPost)
	router.Handle(s.cfg().Daemon.HTTP.AgentsRunPath, withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleAgentsRun)))).Methods(http.MethodPost)
	router.Handle("/api/run", withTimeout(http.HandlerFunc(s.handleAgentsRun))).Methods(http.MethodPost)

	// Observability API — read-only endpoints served unauthenticated at the
	// daemon level. The embedded UI makes same-origin fetch/EventSource calls
	// that cannot attach a Bearer token (EventSource in particular has no
	// header API), so daemon-level auth would break the dashboard whenever
	// api_key is set. Access control for these endpoints is the reverse
	// proxy's responsibility, consistent with the original issue design.
	// The mutation endpoint (/agents/run) retains its Bearer-token gate.
	router.Handle("/api/agents", withTimeout(http.HandlerFunc(s.handleAPIAgents))).Methods(http.MethodGet)
	router.Handle("/api/config", withTimeout(http.HandlerFunc(s.handleAPIConfig))).Methods(http.MethodGet)
	router.Handle("/api/dispatches", withTimeout(http.HandlerFunc(s.handleAPIDispatches))).Methods(http.MethodGet)

	// Extended observability endpoints — only registered when an observe.Store
	// has been attached via WithObserve.
	if s.observeStore != nil {
		router.Handle("/api/events", withTimeout(http.HandlerFunc(s.handleAPIEvents))).Methods(http.MethodGet)
		router.HandleFunc("/api/events/stream", s.handleAPIEventsStream)           // SSE — no timeout
		router.Handle("/api/traces", withTimeout(http.HandlerFunc(s.handleAPITraces))).Methods(http.MethodGet)
		router.HandleFunc("/api/traces/stream", s.handleAPITracesStream)           // SSE — no timeout
		router.Handle("/api/traces/{root_event_id}", withTimeout(http.HandlerFunc(s.handleAPITrace))).Methods(http.MethodGet)
		router.Handle("/api/graph", withTimeout(http.HandlerFunc(s.handleAPIGraph))).Methods(http.MethodGet)
		router.Handle("/api/memory/{agent}/{repo}", withTimeout(http.HandlerFunc(s.handleAPIMemory))).Methods(http.MethodGet)
		router.HandleFunc("/api/memory/stream", s.handleAPIMemoryStream)           // SSE — no timeout
	}

	// Write API — only registered when a SQLite database is attached via
	// WithStore. These endpoints mutate the DB and refresh the server's live
	// config view; they require the same bearer-token as /agents/run.
	if s.db != nil {
		mustWrite := func(h http.Handler) http.Handler {
			return withTimeout(s.requireAPIKey(h))
		}
		router.Handle("/api/agents", mustWrite(http.HandlerFunc(s.handlePutAgent))).Methods(http.MethodPut)
		router.Handle("/api/agents/{name}", mustWrite(http.HandlerFunc(s.handleDeleteAgent))).Methods(http.MethodDelete)
		router.Handle("/api/skills", mustWrite(http.HandlerFunc(s.handlePutSkill))).Methods(http.MethodPut)
		router.Handle("/api/skills/{name}", mustWrite(http.HandlerFunc(s.handleDeleteSkill))).Methods(http.MethodDelete)
		router.Handle("/api/backends", mustWrite(http.HandlerFunc(s.handlePutBackend))).Methods(http.MethodPut)
		router.Handle("/api/backends/{name}", mustWrite(http.HandlerFunc(s.handleDeleteBackend))).Methods(http.MethodDelete)
		router.Handle("/api/repos", mustWrite(http.HandlerFunc(s.handlePutRepo))).Methods(http.MethodPut)
		router.Handle("/api/repos/{name}", mustWrite(http.HandlerFunc(s.handleDeleteRepo))).Methods(http.MethodDelete)
		router.Handle("/api/repos/{name}/bindings", mustWrite(http.HandlerFunc(s.handlePutBinding))).Methods(http.MethodPost)
		router.Handle("/api/repos/{name}/bindings/{id}", mustWrite(http.HandlerFunc(s.handleDeleteBinding))).Methods(http.MethodDelete)
	}

	// Static UI: served from the embedded dist/ tree when a UI FS is provided.
	// Unauthenticated — same reasoning as the /api/* routes above.
	if s.uiFS != nil {
		sub, err := fs.Sub(s.uiFS, "dist")
		if err == nil {
			fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
			router.PathPrefix("/ui/").Handler(withTimeout(fileServer))
			// Redirect the slashless entrypoint /ui → /ui/ so operators and
			// reverse proxies that normalise trailing slashes get the dashboard.
			router.Handle("/ui", withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			}))).Methods(http.MethodGet)
		}
	}

	if s.proxy != nil {
		// The proxy enforces its own upstream timeout via an http.Client
		// deadline; wrapping it with http.TimeoutHandler would impose a hard
		// cap shorter than the configured LLM inference timeout and break long
		// completions.
		router.Handle(s.cfg().Daemon.Proxy.Path, s.proxy).Methods(http.MethodPost)
		// /v1/models is a lightweight stub — wrap it with the standard timeout.
		router.Handle("/v1/models", withTimeout(http.HandlerFunc(s.proxy.ModelsHandler))).Methods(http.MethodGet)
		s.logger.Info().Str("path", s.cfg().Daemon.Proxy.Path).Str("upstream", s.cfg().Daemon.Proxy.Upstream.URL).Msg("anthropic proxy enabled")
	}
	return router
}

func (s *Server) Run(ctx context.Context) error {
	router := s.buildHandler()

	srv := &http.Server{
		Addr:         s.cfg().Daemon.HTTP.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(s.cfg().Daemon.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(s.cfg().Daemon.HTTP.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(s.cfg().Daemon.HTTP.IdleTimeoutSeconds) * time.Second,
	}

	// A background goroutine watches for ctx cancellation and triggers HTTP
	// graceful shutdown. ListenAndServe returns ErrServerClosed once Shutdown
	// completes, at which point we return the Shutdown error from errCh.
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg().Daemon.HTTP.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	logEvent := s.logger.Info().Str("addr", s.cfg().Daemon.HTTP.ListenAddr).Str("status_path", s.cfg().Daemon.HTTP.StatusPath).Str("webhook_path", s.cfg().Daemon.HTTP.WebhookPath).Str("agents_run_path", s.cfg().Daemon.HTTP.AgentsRunPath)
	if s.proxy != nil {
		logEvent = logEvent.Str("proxy_path", s.cfg().Daemon.Proxy.Path)
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
		if s.cfg().Daemon.HTTP.APIKey != "" {
			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := authHeader[len(prefix):]
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg().Daemon.HTTP.APIKey)) != 1 {
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
	if s.cfg().Daemon.HTTP.APIKey == "" {
		http.Error(w, "endpoint disabled: no API key configured", http.StatusForbidden)
		return
	}
	var req agentsRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg().Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Repo == "" {
		http.Error(w, "agent and repo fields are required", http.StatusBadRequest)
		return
	}

	repo, ok := s.cfg().RepoByName(req.Repo)
	if !ok || !repo.Enabled {
		http.Error(w, "repo not found or disabled", http.StatusNotFound)
		return
	}

	ev := workflow.Event{
		ID:    workflow.GenEventID(),
		Repo:  workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:  "agents.run",
		Actor: "human",
		Payload: map[string]any{
			"target_agent": req.Agent,
		},
	}

	if err := s.channels.PushEvent(r.Context(), ev); err != nil {
		s.logger.Error().Err(err).Str("agent", req.Agent).Str("repo", req.Repo).Msg("failed to enqueue on-demand agent run")
		http.Error(w, "event queue full", http.StatusServiceUnavailable)
		return
	}

	s.logger.Info().Str("agent", req.Agent).Str("repo", req.Repo).Str("event_id", ev.ID).Msg("on-demand agent run queued")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "queued",
		"agent":    req.Agent,
		"repo":     req.Repo,
		"event_id": ev.ID,
	})
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg().Daemon.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(body, s.cfg().Daemon.HTTP.WebhookSecret, r.Header.Get("X-Hub-Signature-256")) {
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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

	repo, ok := s.cfg().RepoByName(payload.Repository.FullName)
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
