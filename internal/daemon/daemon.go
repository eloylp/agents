// Package daemon owns the agents daemon as a single composed unit: every
// runtime component (event channels, workflow engine, scheduler, observe
// store, processor, dispatcher), every HTTP handler (fleet, repos, config,
// observe, webhook), the MCP server, the optional proxy, and the embedded
// UI mount. The HTTP listener is one of its goroutines, not its identity —
// the type is named *Daemon because it is the daemon.
//
// Construction is one call: daemon.New(cfg, db, logger) wires every
// component together and returns a fully-formed *Daemon. There are no
// With-setters and no optional fields — production wires the same shape
// every time and the binary ships as one process, so the type is allowed
// to know about its components concretely. The cross-cutting interfaces
// this package used to declare (CronReloader, StatusProvider, MemoryReader,
// …) lived only to support per-test stubs and have been removed; tests
// construct a real *Daemon against a tempdir SQLite via internal/daemon/
// daemontest, the same fixture pattern internal/mcp uses.
//
// The daemon-wide write epoch (the lock that serializes every CRUD write
// across domains, the in-memory cfg pointer, the post-write reload chain)
// lives in internal/coordinator. *Daemon holds a *coordinator.Coordinator
// and exposes Config() as a convenience for callers that already hold a
// *Daemon reference (the MCP get_status tool, mostly); domain handlers
// receive the coordinator directly.
package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/coordinator"
	"github.com/eloylp/agents/internal/fleet"
	mcpserver "github.com/eloylp/agents/internal/mcp"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	daemonconfig "github.com/eloylp/agents/internal/daemon/config"
	daemonfleet "github.com/eloylp/agents/internal/daemon/fleet"
	daemonobserve "github.com/eloylp/agents/internal/daemon/observe"
	daemonrepos "github.com/eloylp/agents/internal/daemon/repos"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/ui"
	"github.com/eloylp/agents/internal/webhook"
	"github.com/eloylp/agents/internal/workflow"
)

// Daemon bundles the agents process. Constructed once via New; Run blocks until the
// supplied context is cancelled and then drives a graceful shutdown.
type Daemon struct {
	logger zerolog.Logger

	// Coordinator owns the daemon-wide write epoch + the cfg pointer.
	coord *coordinator.Coordinator

	// Runtime collaborators.
	channels  *workflow.DataChannels
	engine    *workflow.Engine
	scheduler *scheduler.Scheduler
	processor *workflow.Processor
	obs       *observe.Store

	// HTTP-side concrete handlers. All non-nil after New returns.
	fleet     *daemonfleet.Handler
	repos     *daemonrepos.Handler
	config    *daemonconfig.Handler
	observe   *daemonobserve.Handler
	webhook   *webhook.Handler
	delivery  *webhook.DeliveryStore
	mcp       http.Handler
	proxy     *anthropicproxy.Handler
	uiFS      fs.FS

	startTime time.Time
}

// New builds the daemon. cfg is the initial config snapshot (typically
// loaded from SQLite via store.LoadAndValidate); db is the live SQLite
// handle the caller owns and closes. New constructs every runtime
// component, wires the hot-reload chain through the coordinator, and
// returns a Server ready for Run.
func New(cfg *config.Config, db *sql.DB, logger zerolog.Logger) (*Daemon, error) {
	httpLogger := logger.With().Str("component", "http_server").Logger()

	// Runtime tier first — everything below depends on these.
	runners := buildRunners(cfg, logger)
	channels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer)
	engine := workflow.NewEngine(cfg, runners, channels, logger)

	memBackend := coordinator.NewSQLiteMemory(db)
	engine.WithMemory(memBackend)

	obs := observe.NewStore(db)
	engine.WithTraceRecorder(obs)
	engine.WithGraphRecorder(obs)
	engine.WithRunTracker(obs.ActiveRuns)
	engine.WithStepRecorder(obs)
	memBackend.SetChangeNotifier(obs.PublishMemoryChange)

	sched, err := scheduler.NewScheduler(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	sched.WithEventQueue(channels)
	engine.WithLastRunRecorder(sched)

	// Coordinator owns the cfg pointer + write epoch + reload chain. Built
	// after the runtime so its ReloadFunc can close over the live engine
	// and scheduler. The fleet handler attaches its orphan-cache refresh
	// below as the post-reload hook.
	reloadRecipe := makeReloadRecipe(engine, sched, runnerBuilder(logger), httpLogger)
	coord := coordinator.New(cfg, db, reloadRecipe, nil) // onPost wired below

	// Domain handlers (HTTP layer). All take *coordinator.Coordinator for
	// the write epoch + Config; runtime-side concrete pointers for the
	// scheduling / observability / dispatch info their views need.
	fleetHandler := daemonfleet.New(coord, sched, obs, logger)
	fleetHandler.RefreshOrphansFromCfg(cfg)
	coord.SetPostReload(fleetHandler.RefreshOrphansFromCfg)

	reposHandler := daemonrepos.New(coord, logger)
	configHandler := daemonconfig.New(coord, logger)

	memReader := coordinator.NewSQLiteMemoryReader(db)
	observeHandler := daemonobserve.New(obs, coord, sched, engine, memReader, logger)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.Daemon.HTTP.DeliveryTTLSeconds) * time.Second)
	webhookHandler := webhook.NewHandler(deliveryStore, channels, coord, logger)

	// Processor sits over the queue; event recorder writes into observe so
	// /events shows the firehose.
	workers := cfg.Daemon.Processor.MaxConcurrentAgents
	shutdown := time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds) * time.Second
	processor := workflow.NewProcessor(channels, engine, workers, shutdown, logger)
	processor.WithEventRecorder(obs)

	d := &Daemon{
		logger:    httpLogger,
		coord:     coord,
		channels:  channels,
		engine:    engine,
		scheduler: sched,
		processor: processor,
		obs:       obs,
		fleet:     fleetHandler,
		repos:     reposHandler,
		config:    configHandler,
		observe:   observeHandler,
		webhook:   webhookHandler,
		delivery:  deliveryStore,
		uiFS:      ui.FS,
		startTime: time.Now(),
	}

	if cfg.Daemon.Proxy.Enabled {
		up := cfg.Daemon.Proxy.Upstream
		d.proxy = anthropicproxy.NewHandler(anthropicproxy.UpstreamConfig{
			URL:       up.URL,
			Model:     up.Model,
			APIKey:    up.APIKey,
			Timeout:   time.Duration(up.TimeoutSeconds) * time.Second,
			ExtraBody: up.ExtraBody,
		}, logger)
	}

	// MCP last — it consumes every other component. Constructed here so the
	// handler picks up exactly the wiring the REST surface uses.
	d.mcp = mcpserver.New(mcpserver.Deps{
		DB:         db,
		Coord:      coord,
		StatusJSON: d.StatusJSON,
		Queue:      channels,
		Observe:    obs,
		Engine:     engine,
		Fleet:      fleetHandler,
		Repos:      reposHandler,
		Config:     configHandler,
		Logger:     logger,
	})

	return d, nil
}

// Config delegates to the coordinator. Convenience for callers (notably the
// MCP get_status tool) that already hold *Daemon.
func (d *Daemon) Config() *config.Config { return d.coord.Config() }

// DB returns the SQLite handle. Exported for tests; production callers use
// the domain handlers.
func (d *Daemon) DB() *sql.DB { return d.coord.DB() }

// Coordinator returns the daemon's coordinator. Exported for tests that need
// to call Do() directly without going through an HTTP route.
func (d *Daemon) Coordinator() *coordinator.Coordinator { return d.coord }

// Engine returns the workflow engine. Exported for tests asserting on
// dispatch counters or active-run state.
func (d *Daemon) Engine() *workflow.Engine { return d.engine }

// Channels returns the daemon's data channels. Exported for tests that
// assert events were enqueued by reading directly off EventChan.
func (d *Daemon) Channels() *workflow.DataChannels { return d.channels }

// Fleet returns the fleet HTTP handler. Exported for tests that need to
// invoke fleet handlers directly (e.g., to assert on the orphan cache).
func (d *Daemon) Fleet() *daemonfleet.Handler { return d.fleet }

// Repos returns the repos HTTP handler. Exported for tests.
func (d *Daemon) Repos() *daemonrepos.Handler { return d.repos }

// ConfigHandler returns the /config HTTP handler. Exported for tests.
func (d *Daemon) ConfigHandler() *daemonconfig.Handler { return d.config }

// Scheduler returns the scheduler. Exported for tests.
func (d *Daemon) Scheduler() *scheduler.Scheduler { return d.scheduler }

// Observe returns the observability store. Exported for tests that seed
// trace / event rows directly via observe.Store methods.
func (d *Daemon) Observe() *observe.Store { return d.obs }

// Handler builds and returns the HTTP router. Exported for tests that
// exercise routing without starting a real TCP listener.
func (d *Daemon) Handler() http.Handler { return d.buildRouter() }

// Run starts every long-running goroutine the daemon needs (delivery store
// eviction, dispatch dedup eviction, the worker pool, the cron scheduler,
// the HTTP listener) and blocks until ctx is cancelled. Graceful shutdown
// drains the HTTP server first, then the worker pool drains its in-flight
// events; cron entries stop firing immediately.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Info().Msg("starting agents daemon")

	d.delivery.Start(ctx)
	d.engine.StartDispatchDedup(ctx)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return d.processor.Run(groupCtx) })
	group.Go(func() error { return d.scheduler.Run(groupCtx) })
	group.Go(func() error { return d.runHTTP(groupCtx) })

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	d.logger.Info().Msg("agents daemon stopped")
	return nil
}

func (d *Daemon) runHTTP(ctx context.Context) error {
	cfg := d.coord.Config()
	router := d.buildRouter()

	srv := &http.Server{
		Addr:         cfg.Daemon.HTTP.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(cfg.Daemon.HTTP.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.Daemon.HTTP.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(cfg.Daemon.HTTP.IdleTimeoutSeconds) * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	logEvent := d.logger.Info().Str("addr", cfg.Daemon.HTTP.ListenAddr).Str("status_path", cfg.Daemon.HTTP.StatusPath).Str("webhook_path", cfg.Daemon.HTTP.WebhookPath)
	if d.proxy != nil {
		logEvent = logEvent.Str("proxy_path", cfg.Daemon.Proxy.Path)
	}
	logEvent.Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

func (d *Daemon) buildRouter() http.Handler {
	cfg := d.coord.Config()

	writeTimeout := time.Duration(cfg.Daemon.HTTP.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}

	router := mux.NewRouter()
	router.Handle(cfg.Daemon.HTTP.StatusPath, withTimeout(http.HandlerFunc(d.handleStatus))).Methods(http.MethodGet)
	router.Handle("/run", withTimeout(http.HandlerFunc(d.handleAgentsRun))).Methods(http.MethodPost)
	d.webhook.RegisterRoutes(router, withTimeout)

	// /agents merges the read-only fleet snapshot view (GET) with CRUD create
	// (POST) on a single mux entry.
	router.Handle("/agents", withTimeout(http.HandlerFunc(d.handleAgents))).Methods(http.MethodGet, http.MethodPost)

	d.fleet.RegisterRoutes(router, withTimeout)
	d.repos.RegisterRoutes(router, withTimeout)
	d.config.RegisterRoutes(router, withTimeout)
	d.observe.RegisterRoutes(router, withTimeout)

	if d.uiFS != nil {
		if sub, err := fs.Sub(d.uiFS, "dist"); err == nil {
			fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
			router.PathPrefix("/ui/").Handler(withTimeout(fileServer))
			router.Handle("/ui", withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			}))).Methods(http.MethodGet)
			router.Handle("/", withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			}))).Methods(http.MethodGet)
		}
	}

	if d.proxy != nil {
		// Proxy enforces its own upstream timeout via http.Client; wrapping
		// with http.TimeoutHandler would impose a hard cap shorter than the
		// configured LLM inference timeout and break long completions.
		router.Handle(cfg.Daemon.Proxy.Path, d.proxy).Methods(http.MethodPost)
		router.Handle("/v1/models", withTimeout(http.HandlerFunc(d.proxy.ModelsHandler))).Methods(http.MethodGet)
		d.logger.Info().Str("path", cfg.Daemon.Proxy.Path).Str("upstream", cfg.Daemon.Proxy.Upstream.URL).Msg("anthropic proxy enabled")
	}
	if d.mcp != nil {
		// MCP streamable-HTTP can hold SSE streams open; do not wrap in
		// http.TimeoutHandler. Per-tool work is bounded inside internal/mcp.
		router.PathPrefix("/mcp").Handler(d.mcp)
		d.logger.Info().Str("path", "/mcp").Msg("mcp server enabled")
	}
	return router
}

// handleAgents dispatches GET to the fleet snapshot view and POST to the
// fleet handler's CRUD create. Single mux entry covers both methods.
func (d *Daemon) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		d.fleet.HandleAgentsView(w, r)
		return
	}
	d.fleet.HandleAgentsCreate(w, r)
}

// ── /status ───────────────────────────────────────────────────────────────

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
	Agents         []scheduler.AgentStatus    `json:"agents"`
	Dispatch       *workflow.DispatchStats    `json:"dispatch,omitempty"`
	OrphanedAgents statusOrphanSummaryJSON    `json:"orphaned_agents"`
}

// buildStatus assembles the /status payload. Same struct backs the
// MCP get_status tool so both surfaces share one source of truth.
func (d *Daemon) buildStatus() statusJSON {
	q := d.channels.QueueStats()

	resp := statusJSON{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(d.startTime).Seconds()),
		Queues: map[string]statusQueueJSON{
			"events": {Buffered: q.Buffered, Capacity: q.Capacity},
		},
		Agents: append([]scheduler.AgentStatus{}, d.scheduler.AgentStatuses()...),
	}

	orphans := d.fleet.OrphansSnapshot()
	if fresh, err := d.fleet.RefreshOrphansFromDB(); err != nil {
		d.logger.Warn().Err(err).Msg("status: orphan snapshot refresh failed")
	} else {
		orphans = fresh
	}
	resp.OrphanedAgents = statusOrphanSummaryJSON{Count: orphans.Count}
	if !orphans.GeneratedAt.IsZero() {
		at := orphans.GeneratedAt
		resp.OrphanedAgents.UpdatedAt = &at
	}

	stats := d.engine.DispatchStats()
	resp.Dispatch = &stats
	return resp
}

// StatusJSON returns the /status payload as JSON bytes. The MCP
// get_status tool consumes this so the wire shape stays in lockstep.
func (d *Daemon) StatusJSON() ([]byte, error) {
	return json.Marshal(d.buildStatus())
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d.buildStatus())
}

// ── /run ──────────────────────────────────────────────────────────────────

type agentsRunRequest struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

func (d *Daemon) handleAgentsRun(w http.ResponseWriter, r *http.Request) {
	cfg := d.coord.Config()
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

	if err := d.channels.PushEvent(r.Context(), ev); err != nil {
		d.logger.Error().Err(err).Str("agent", req.Agent).Str("repo", req.Repo).Msg("failed to enqueue on-demand agent run")
		http.Error(w, "event queue full", http.StatusServiceUnavailable)
		return
	}

	d.logger.Info().Str("agent", req.Agent).Str("repo", req.Repo).Str("event_id", ev.ID).Msg("on-demand agent run queued")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "queued",
		"agent":    req.Agent,
		"repo":     req.Repo,
		"event_id": ev.ID,
	})
}

// ── helpers: runner construction + reload recipe ──────────────────────────

// runnerBuilder returns the factory used to construct ai.Runner instances at
// startup AND on every CRUD-triggered backend change. The same factory in
// both paths means hot-reloaded backends produce runners bit-for-bit
// identical to startup-built ones.
func runnerBuilder(logger zerolog.Logger) func(name string, b fleet.Backend) ai.Runner {
	return func(name string, b fleet.Backend) ai.Runner {
		return ai.NewCommandRunner(
			name, "command", b.Command, backendEnvOverrides(b),
			b.TimeoutSeconds, b.MaxPromptChars, b.RedactionSaltEnv,
			logger,
		)
	}
}

func backendEnvOverrides(b fleet.Backend) map[string]string {
	if b.LocalModelURL == "" {
		return nil
	}
	return map[string]string{"ANTHROPIC_BASE_URL": b.LocalModelURL}
}

func buildRunners(cfg *config.Config, logger zerolog.Logger) map[string]ai.Runner {
	build := runnerBuilder(logger)
	out := make(map[string]ai.Runner, len(cfg.Daemon.AIBackends))
	for name, b := range cfg.Daemon.AIBackends {
		out[name] = build(name, b)
	}
	return out
}

// makeReloadRecipe returns the coordinator.ReloadFunc that propagates each
// post-write cfg snapshot to the runtime in lockstep: rebuild runners,
// engine.UpdateConfigAndRunners (atomic cfg+runners swap so concurrent
// runAgent calls cannot observe a torn pair), refresh the dispatcher's
// agent map, then ask the scheduler to swap its cron entries. Each step
// is one line; the recipe lives in one place.
func makeReloadRecipe(engine *workflow.Engine, sched *scheduler.Scheduler, build func(name string, b fleet.Backend) ai.Runner, logger zerolog.Logger) coordinator.ReloadFunc {
	return func(cfg *config.Config) error {
		runners := make(map[string]ai.Runner, len(cfg.Daemon.AIBackends))
		for name, b := range cfg.Daemon.AIBackends {
			runners[name] = build(name, b)
		}
		engine.UpdateConfigAndRunners(cfg, runners)
		if d := engine.Dispatcher(); d != nil {
			d.UpdateAgents(cfg.Agents)
		}
		if err := sched.RebuildCron(cfg.Repos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
			logger.Error().Err(err).Msg("hot reload: scheduler rebuild failed")
			return err
		}
		return nil
	}
}

// LoadConfig is the daemon's startup config-load helper, kept here (rather
// than in cmd/agents/main.go) so the server package owns the full daemon
// boot path. It opens the SQLite database, optionally imports a YAML file
// into it, runs auto-discovery for AI backends if none are configured,
// and returns the validated *config.Config. The caller owns and closes db.
// Status/import progress messages are written to msg (typically os.Stderr);
// pass nil to silence them.
func LoadConfig(ctx context.Context, dbPath, importPath string, msg io.Writer) (*config.Config, *sql.DB, error) {
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	if importPath != "" {
		yamlCfg, err := config.Load(importPath)
		if err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("import: load YAML: %w", err)
		}
		if err := store.Import(db, yamlCfg); err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("import: write to database: %w", err)
		}
		if msg != nil {
			nBindings := 0
			for _, r := range yamlCfg.Repos {
				nBindings += len(r.Use)
			}
			fmt.Fprintf(msg, "import: imported %d backends, %d skills, %d agents, %d repos, %d bindings\n",
				len(yamlCfg.Daemon.AIBackends), len(yamlCfg.Skills),
				len(yamlCfg.Agents), len(yamlCfg.Repos), nBindings)
		}
	}
	autoDiscovered, _, err := backends.AutoDiscoverIfBackendsMissing(ctx, db)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("auto-discover backends: %w", err)
	}
	if autoDiscovered && msg != nil {
		fmt.Fprintln(msg, "startup: discovered AI backends from local CLI tools")
	}
	cfg, err := store.LoadAndValidate(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return cfg, db, nil
}
