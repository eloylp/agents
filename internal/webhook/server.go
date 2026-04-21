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
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/workflow"
)

// CronReloader is implemented by *autonomous.Scheduler. It is called after a
// repo, agent, skill, or backend write to update the scheduler's in-process
// state without restarting the daemon.
type CronReloader interface {
	Reload(repos []config.RepoDef, agents []config.AgentDef, skills map[string]config.SkillDef, backends map[string]config.AIBackendConfig) error
}

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

// ErrMemoryNotFound is returned by MemoryReader.ReadMemory when no memory
// record exists for the requested (agent, repo) pair. Callers should use
// errors.Is to distinguish a missing record (404) from a genuine I/O error.
var ErrMemoryNotFound = errors.New("webhook: memory not found")

// MemoryReader retrieves the stored memory for an (agent, repo) pair.
// The webhook server uses this interface to serve /api/memory/{agent}/{repo}
// without knowing whether the backing store is the filesystem or SQLite.
// ReadMemory returns ErrMemoryNotFound when the record does not exist; it
// returns ("", time.Time{}, nil) when the record exists but the content is
// empty. The returned time.Time is the last-updated timestamp used to set the
// X-Memory-Mtime response header; a zero value means the timestamp is unknown.
type MemoryReader interface {
	ReadMemory(agent, repo string) (string, time.Time, error)
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
	proxy         *anthropicproxy.Handler
	uiFS          fs.FS          // optional; when set, /ui/ serves these static files
	observeStore  *observe.Store // optional; when set, enables observability endpoints
	db            *sql.DB        // optional; when set, enables /api/store/* CRUD endpoints
	cronReloader  CronReloader   // optional; called after repo/agent writes to reload cron
	memReader     MemoryReader   // optional; when set, /api/memory reads from this (SQLite mode)
	// storeMu serializes the "DB write → snapshot read → in-memory Reload"
	// sequence so that concurrent write requests cannot interleave their
	// snapshots and leave the scheduler in a stale or inconsistent state.
	storeMu sync.Mutex
	// cfgMu protects s.cfg from data races between the hot-reload write path
	// (reloadCron, called under storeMu) and concurrent handler reads. Use
	// loadCfg() to read and reloadCron() to update. Static daemon config
	// (HTTP, proxy, log) is never replaced after startup, but since the entire
	// pointer is swapped on reload the lock is required for all accesses.
	cfgMu sync.RWMutex
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

// WithStore attaches a SQLite database and an optional CronReloader.
// When set, the server registers /api/store/* CRUD endpoints for agents,
// skills, backends, and repos. Writes to repos or agents also call
// r.Reload so that cron schedules take effect immediately. r may be nil
// if hot-reload is not needed.
func (s *Server) WithStore(db *sql.DB, r CronReloader) {
	s.db = db
	s.cronReloader = r
}

// WithMemoryReader attaches a MemoryReader used by /api/memory/{agent}/{repo}
// when the daemon is running in --db mode. When not set, the endpoint falls
// back to reading from the filesystem memory_dir.
func (s *Server) WithMemoryReader(r MemoryReader) {
	s.memReader = r
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, channels EventQueue, provider StatusProvider, dispatchStats DispatchStatsProvider, logger zerolog.Logger) *Server {
	s := &Server{
		cfg:           cfg,
		delivery:      delivery,
		logger:        logger.With().Str("component", "webhook_server").Logger(),
		channels:      channels,
		provider:      provider,
		dispatchStats: dispatchStats,
		startTime:     time.Now(),
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

// loadCfg returns the current config snapshot. It is safe to call
// concurrently with reloadCron, which may swap s.cfg under cfgMu.Lock().
// Callers should snapshot once per handler invocation and use the returned
// pointer throughout so they observe a single consistent config epoch.
func (s *Server) loadCfg() *config.Config {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return cfg
}

// buildHandler constructs the HTTP router for the server and returns it as
// an http.Handler. It is separated from Run so tests can exercise routing
// without starting a real TCP listener.
func (s *Server) buildHandler() http.Handler {
	// Snapshot the config once under the read lock so that concurrent
	// reloadCron calls (which swap s.cfg under cfgMu.Lock) cannot race with
	// the reads below.
	cfg := s.loadCfg()

	// http.TimeoutHandler bounds handler execution time (i.e. how long the
	// handler function runs before it must start writing). It is NOT a
	// replacement for http.Server.WriteTimeout, which enforces a socket write
	// deadline and is still set in Run(). SSE handlers clear that write
	// deadline for themselves via http.ResponseController.SetWriteDeadline so
	// they can stream indefinitely; see serveSSEWithInterval in api.go.
	writeTimeout := time.Duration(cfg.Daemon.HTTP.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}

	router := mux.NewRouter()
	router.Handle(cfg.Daemon.HTTP.StatusPath, withTimeout(http.HandlerFunc(s.handleStatus))).Methods(http.MethodGet)
	router.Handle(cfg.Daemon.HTTP.WebhookPath, withTimeout(http.HandlerFunc(s.handleGitHubWebhook))).Methods(http.MethodPost)
	router.Handle(cfg.Daemon.HTTP.AgentsRunPath, withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleAgentsRun)))).Methods(http.MethodPost)
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

	// Store CRUD endpoints — only registered when a SQLite store has been
	// attached via WithStore (i.e. when the daemon was started with --db).
	// Read (GET) endpoints are served unauthenticated, consistent with the
	// other observability API endpoints. Write (POST / DELETE) endpoints are
	// gated by requireAPIKey because they mutate live fleet configuration.
	if s.db != nil {
		router.Handle("/api/store/agents", withTimeout(http.HandlerFunc(s.handleStoreAgents))).Methods(http.MethodGet)
		router.Handle("/api/store/agents", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreAgents)))).Methods(http.MethodPost)
		router.Handle("/api/store/agents/{name}", withTimeout(http.HandlerFunc(s.handleStoreAgent))).Methods(http.MethodGet)
		router.Handle("/api/store/agents/{name}", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreAgent)))).Methods(http.MethodDelete)
		router.Handle("/api/store/skills", withTimeout(http.HandlerFunc(s.handleStoreSkills))).Methods(http.MethodGet)
		router.Handle("/api/store/skills", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreSkills)))).Methods(http.MethodPost)
		router.Handle("/api/store/skills/{name}", withTimeout(http.HandlerFunc(s.handleStoreSkill))).Methods(http.MethodGet)
		router.Handle("/api/store/skills/{name}", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreSkill)))).Methods(http.MethodDelete)
		router.Handle("/api/store/backends", withTimeout(http.HandlerFunc(s.handleStoreBackends))).Methods(http.MethodGet)
		router.Handle("/api/store/backends", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreBackends)))).Methods(http.MethodPost)
		router.Handle("/api/store/backends/{name}", withTimeout(http.HandlerFunc(s.handleStoreBackend))).Methods(http.MethodGet)
		router.Handle("/api/store/backends/{name}", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreBackend)))).Methods(http.MethodDelete)
		router.Handle("/api/store/repos", withTimeout(http.HandlerFunc(s.handleStoreRepos))).Methods(http.MethodGet)
		router.Handle("/api/store/repos", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreRepos)))).Methods(http.MethodPost)
		// {owner}/{repo} captures "owner/repo" across two path segments.
		router.Handle("/api/store/repos/{owner}/{repo}", withTimeout(http.HandlerFunc(s.handleStoreRepo))).Methods(http.MethodGet)
		router.Handle("/api/store/repos/{owner}/{repo}", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreRepo)))).Methods(http.MethodDelete)
		// Export (GET) requires the API key because backends may hold env secrets.
		router.Handle("/api/store/export", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreExport)))).Methods(http.MethodGet)
		router.Handle("/api/store/import", withTimeout(s.requireAPIKey(http.HandlerFunc(s.handleStoreImport)))).Methods(http.MethodPost)
	}

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
		router.Handle(cfg.Daemon.Proxy.Path, s.proxy).Methods(http.MethodPost)
		// /v1/models is a lightweight stub — wrap it with the standard timeout.
		router.Handle("/v1/models", withTimeout(http.HandlerFunc(s.proxy.ModelsHandler))).Methods(http.MethodGet)
		s.logger.Info().Str("path", cfg.Daemon.Proxy.Path).Str("upstream", cfg.Daemon.Proxy.Upstream.URL).Msg("anthropic proxy enabled")
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
		cfg := s.loadCfg()
		if cfg.Daemon.HTTP.APIKey != "" {
			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := authHeader[len(prefix):]
			if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.Daemon.HTTP.APIKey)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
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
	cfg := s.loadCfg()
	if cfg.Daemon.HTTP.APIKey == "" {
		http.Error(w, "endpoint disabled: no API key configured", http.StatusForbidden)
		return
	}
	var req agentsRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Repo == "" {
		http.Error(w, "agent and repo fields are required", http.StatusBadRequest)
		return
	}

	repo, ok := cfg.RepoByName(req.Repo)
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
	cfg := s.loadCfg()
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.Daemon.HTTP.MaxBodyBytes))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(body, cfg.Daemon.HTTP.WebhookSecret, r.Header.Get("X-Hub-Signature-256")) {
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
	cfg := s.loadCfg()
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

	repo, ok := cfg.RepoByName(payload.Repository.FullName)
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
