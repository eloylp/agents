package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/server"
	serverconfig "github.com/eloylp/agents/internal/server/config"
	serverrepos "github.com/eloylp/agents/internal/server/repos"
	"github.com/eloylp/agents/internal/workflow"
)

type Server struct {
	cfg           *config.Config
	delivery      *DeliveryStore
	logger        zerolog.Logger
	channels      server.EventQueue
	h             *Handler // GitHub webhook receiver — owns event parsing, HMAC, dedupe
	provider      server.StatusProvider
	runtimeState  server.RuntimeStateProvider // optional; used by /api/agents for live run status
	dispatchStats server.DispatchStatsProvider
	startTime     time.Time
	proxy         *anthropicproxy.Handler
	uiFS          fs.FS               // optional; when set, /ui/ serves these static files
	observeStore  *observe.Store      // optional; when set, enables observability endpoints
	db            *sql.DB             // optional; when set, enables /api/store/* CRUD endpoints
	cronReloader  server.CronReloader // optional; called after repo/agent writes to reload cron
	memReader     server.MemoryReader // optional; when set, /api/memory reads from this (SQLite mode)
	mcp           http.Handler        // optional; when set, /mcp serves this MCP handler
	// observeRegister mounts the observability routes when set via
	// WithObserveRegister. cmd/agents constructs the observe handler
	// externally so this server doesn't import internal/server/observe.
	observeRegister func(*mux.Router, func(http.Handler) http.Handler)
	// fleet is the registered fleet handler. cmd/agents constructs the
	// concrete *internal/server/fleet.Handler externally and supplies it
	// (along with cross-domain hooks below) via WithFleet so this package
	// stays free of any dependency on internal/server/fleet.
	fleet            server.HandlerRegister
	agentsDispatcher http.HandlerFunc                                 // GET vs POST /agents — supplied by WithFleet
	orphansSource    server.OrphansSource                             // /status orphan summary — supplied by WithFleet
	onConfigReload   func(*config.Config)                             // reloadCron post-hook — supplied by WithFleet
	repos            *serverrepos.Handler                             // constructed in WithStore; nil until then
	config           *serverconfig.Handler                            // constructed in NewServer (cfg-only mode); db wired by WithStore
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

// WithObserve stores the observability store reference. Kept for backward
// compatibility — the observability routes themselves are now mounted via
// WithObserveRegister, which cmd/agents calls with a closure that
// constructs the internal/server/observe.Handler. This setter remains a
// useful place to record the store for surfaces beyond HTTP that need
// access to it.
func (s *Server) WithObserve(store *observe.Store) {
	s.observeStore = store
}

// WithObserveRegister installs a closure that mounts the observability
// surface on the router. The composing caller (cmd/agents) constructs the
// internal/server/observe.Handler externally so this package stays free of
// any dependency on it.
func (s *Server) WithObserveRegister(register func(*mux.Router, func(http.Handler) http.Handler)) {
	s.observeRegister = register
}

// WithRuntimeState records an optional runtime-state provider on the server.
// It is no longer forwarded to the fleet handler — cmd/agents calls
// fleetHandler.SetRuntimeState directly before WithFleet so the wiring stays
// in one place.
func (s *Server) WithRuntimeState(rsp server.RuntimeStateProvider) {
	s.runtimeState = rsp
}

// WithStore attaches a SQLite database and an optional CronReloader. The
// database backs reloadCron and is forwarded to the still-locally-owned
// repos and config handlers. The fleet handler is wired externally by
// cmd/agents (which calls SetDB on it directly before WithFleet), so this
// method no longer touches it.
func (s *Server) WithStore(db *sql.DB, r server.CronReloader) {
	s.db = db
	s.cronReloader = r
	s.repos = serverrepos.New(db, s, s, s.logger)
	if s.config != nil {
		s.config.SetDB(db)
	}
}

// WithFleet registers the fleet handler and the three hooks the composing
// server needs from it: the GET-vs-POST dispatcher for /agents, the orphan
// snapshot source used by /status, and the post-reload callback that
// refreshes the orphan cache after every CRUD write.
//
// cmd/agents constructs the concrete internal/server/fleet.Handler and
// supplies thin adapters here so this package stays free of any
// internal/server/fleet import.
func (s *Server) WithFleet(h server.HandlerRegister, dispatcher http.HandlerFunc, orphans server.OrphansSource, onReload func(*config.Config)) {
	s.fleet = h
	s.agentsDispatcher = dispatcher
	s.orphansSource = orphans
	s.onConfigReload = onReload
}

// Repos returns the repos handler so the daemon's MCP wiring can satisfy the
// mcp.RepoWriter and mcp.BindingWriter interfaces with the same instance the
// HTTP router uses. Nil when WithStore has not been called.
func (s *Server) Repos() *serverrepos.Handler { return s.repos }

// Cfg returns the config handler so the daemon's MCP wiring can satisfy the
// mcp.ConfigBytes / mcp.ConfigImporter interfaces with the same instance the
// HTTP router uses.
func (s *Server) Cfg() *serverconfig.Handler { return s.config }

// Do runs fn under the server's CRUD write lock and, on success, triggers a
// scheduler reload + in-memory config swap. Implements server.WriteCoordinator
// for the domain-scoped handler packages so every CRUD mutation across fleet,
// repos, and config domains shares one lock and one reload epoch.
func (s *Server) Do(fn func() error) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	return s.reloadCron()
}

// WithMemoryReader attaches a MemoryReader used by /api/memory/{agent}/{repo}
// when the daemon is running in --db mode.
func (s *Server) WithMemoryReader(r server.MemoryReader) {
	s.memReader = r
}

// WithMCP attaches an MCP (Model Context Protocol) handler served at /mcp.
// When set, the daemon exposes fleet-management tools to MCP clients over
// Streamable HTTP. When nil (the default), no /mcp route is mounted.
func (s *Server) WithMCP(h http.Handler) {
	s.mcp = h
}

// Config returns the current effective config snapshot under the server's
// lock. Satisfies mcp.ConfigProvider and serverobserve.ConfigGetter so both
// surfaces read the same hot-reloaded config the REST API does.
func (s *Server) Config() *config.Config {
	return s.loadCfg()
}

func NewServer(cfg *config.Config, delivery *DeliveryStore, channels server.EventQueue, provider server.StatusProvider, dispatchStats server.DispatchStatsProvider, logger zerolog.Logger) *Server {
	s := &Server{
		cfg:           cfg,
		delivery:      delivery,
		logger:        logger.With().Str("component", "webhook_server").Logger(),
		channels:      channels,
		provider:      provider,
		dispatchStats: dispatchStats,
		startTime:     time.Now(),
	}
	// Config handler boots in cfg-only mode so /config serves the
	// effective YAML-loaded snapshot before WithStore wires /export +
	// /import. Fleet is wired externally (see WithFleet) by cmd/agents
	// so this package no longer imports internal/server/fleet.
	s.config = serverconfig.New(s, s, s.logger)
	// GitHub webhook receiver — pure event-parsing + HMAC + delivery dedupe.
	// The server delegates handleGitHubWebhook to it so existing tests that
	// call s.handleGitHubWebhook keep working unchanged; future PRs will
	// drop that delegate and mount h.RegisterRoutes directly.
	s.h = NewHandler(delivery, channels, s, logger)
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
	// they can stream indefinitely; see ServeSSEWithInterval in the
	// internal/server/observe package.
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
	router.Handle("/run", withTimeout(http.HandlerFunc(s.handleAgentsRun))).Methods(http.MethodPost)

	// /agents merges the read-only fleet snapshot view (GET) with the CRUD
	// create (POST) on a single mux entry; the dispatcher delegates to the
	// fleet handler for both.
	router.Handle("/agents", withTimeout(http.HandlerFunc(s.handleAgents))).Methods(http.MethodGet, http.MethodPost)

	// Domain handler packages own their routes. Fleet is wired via
	// WithFleet by the composing caller; repos is wired by WithStore.
	if s.fleet != nil {
		s.fleet.RegisterRoutes(router, withTimeout)
	}
	if s.repos != nil {
		s.repos.RegisterRoutes(router, withTimeout)
	}

	// Observability surface lives in its own package; the composing caller
	// (cmd/agents, or a test helper) constructs the handler and supplies a
	// closure via WithObserveRegister.
	if s.observeRegister != nil {
		s.observeRegister(router, withTimeout)
	}

	s.config.RegisterRoutes(router, withTimeout)

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
			router.Handle("/", withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if s.mcp != nil {
		// The MCP streamable HTTP handler can legitimately hold SSE streams
		// open, so we do not wrap it in http.TimeoutHandler. Short-lived tool
		// calls are bounded by the individual tool handlers in internal/mcp.
		router.PathPrefix("/mcp").Handler(s.mcp)
		s.logger.Info().Str("path", "/mcp").Msg("mcp server enabled")
	}
	return router
}

// handleAgents dispatches GET to the fleet snapshot view and POST to the
// fleet handler's CRUD create. The dispatcher closure is supplied by
// WithFleet so this package doesn't import internal/server/fleet.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.agentsDispatcher == nil {
		http.NotFound(w, r)
		return
	}
	s.agentsDispatcher(w, r)
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

	logEvent := s.logger.Info().Str("addr", s.cfg.Daemon.HTTP.ListenAddr).Str("status_path", s.cfg.Daemon.HTTP.StatusPath).Str("webhook_path", s.cfg.Daemon.HTTP.WebhookPath)
	if s.proxy != nil {
		logEvent = logEvent.Str("proxy_path", s.cfg.Daemon.Proxy.Path)
	}
	logEvent.Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

// statusQueueJSON, statusOrphanSummaryJSON, and statusJSON are the wire
// shapes used by both /status and the get_status MCP tool. Keeping them at
// package level means the two surfaces can never drift.
type statusQueueJSON struct {
	Buffered int `json:"buffered"`
	Capacity int `json:"capacity"`
}

type statusOrphanSummaryJSON struct {
	Count     int        `json:"count"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type statusJSON struct {
	Status         string                     `json:"status"`
	UptimeSeconds  int64                      `json:"uptime_seconds"`
	Queues         map[string]statusQueueJSON `json:"queues"`
	Agents         []server.AgentStatus       `json:"agents"`
	Dispatch       *workflow.DispatchStats    `json:"dispatch,omitempty"`
	OrphanedAgents statusOrphanSummaryJSON    `json:"orphaned_agents"`
}

// buildStatus assembles the status payload under a snapshot of the server
// state. Kept separate from handleStatus so the MCP get_status tool can
// serialise the same struct without duplicating the aggregation logic.
func (s *Server) buildStatus() statusJSON {
	q := s.channels.QueueStats()

	agents := []server.AgentStatus{}
	if s.provider != nil {
		if got := s.provider.AgentStatuses(); len(got) > 0 {
			agents = got
		}
	}

	resp := statusJSON{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
		Queues: map[string]statusQueueJSON{
			"events": {Buffered: q.Buffered, Capacity: q.Capacity},
		},
		Agents: agents,
	}
	if s.orphansSource != nil {
		orphans := s.orphansSource.OrphansSnapshot()
		if fresh, err := s.orphansSource.RefreshOrphansFromDB(); err != nil {
			s.logger.Warn().Err(err).Msg("status: orphan snapshot refresh failed")
		} else {
			orphans = fresh
		}
		resp.OrphanedAgents = statusOrphanSummaryJSON{Count: orphans.Count}
		if !orphans.GeneratedAt.IsZero() {
			at := orphans.GeneratedAt
			resp.OrphanedAgents.UpdatedAt = &at
		}
	}
	if s.dispatchStats != nil {
		stats := s.dispatchStats.DispatchStats()
		resp.Dispatch = &stats
	}
	return resp
}

// StatusJSON returns the status payload marshalled as JSON bytes. Used by
// the MCP get_status tool so the two surfaces share a single source of truth.
func (s *Server) StatusJSON() ([]byte, error) {
	return json.Marshal(s.buildStatus())
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.buildStatus())
}

type agentsRunRequest struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

func (s *Server) handleAgentsRun(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadCfg()
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

// handleGitHubWebhook delegates to the package-internal Handler, which
// owns HMAC verification, delivery dedupe, and per-event-type parsing.
// Kept on the Server type so existing tests that call s.handleGitHubWebhook
// directly continue to work; the route in buildHandler still goes through
// here for the same reason. A follow-up PR will mount h.RegisterRoutes
// directly and drop this method.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	s.h.handleGitHubWebhook(w, r)
}

