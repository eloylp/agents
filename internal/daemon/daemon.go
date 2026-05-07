// Package daemon owns the agents daemon as a single composed unit: every
// runtime component (event channels, workflow engine, scheduler, observe
// store, processor, dispatcher), every HTTP handler (fleet, repos, config,
// observe, webhook), the MCP server, the optional proxy, and the embedded
// UI mount. The HTTP listener is one of its goroutines, not its identity ,
// the type is named *Daemon because it is the daemon.
//
// Construction is one call: daemon.New(cfg, db, logger) wires every
// component together and returns a fully-formed *Daemon. There are no
// With-setters and no optional fields, production wires the same shape
// every time and the binary ships as one process, so the type is allowed
// to know about its components concretely. Tests construct a real *Daemon
// against a tempdir SQLite via internal/daemon/daemontest.
//
// State and the database. SQLite is the source of truth. Daemon-level
// config (HTTP, proxy, log, processor) is set at startup and never
// mutates, it lives on the Daemon struct. The four CRUD-mutable entity
// sets (agents, repos, skills, backends) live only in SQLite; every
// runtime component reads them on demand. The scheduler is the one
// exception: robfig/cron requires registered entries to fire, so the
// scheduler holds a registered set in memory and reconciles it against
// SQLite via a polling goroutine. CRUD writes never push updates into
// the runtime, the next read or reconcile picks them up.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	anthropicproxy "github.com/eloylp/agents/internal/anthropic_proxy"
	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/config"
	daemonconfig "github.com/eloylp/agents/internal/daemon/config"
	daemonfleet "github.com/eloylp/agents/internal/daemon/fleet"
	daemonobserve "github.com/eloylp/agents/internal/daemon/observe"
	daemonrepos "github.com/eloylp/agents/internal/daemon/repos"
	daemonrunners "github.com/eloylp/agents/internal/daemon/runners"
	"github.com/eloylp/agents/internal/fleet"
	mcpserver "github.com/eloylp/agents/internal/mcp"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/ui"
	"github.com/eloylp/agents/internal/webhook"
	"github.com/eloylp/agents/internal/workflow"
)

// Daemon bundles the agents process. Constructed once via New; Run blocks
// until the supplied context is cancelled and then drives a graceful
// shutdown.
type Daemon struct {
	logger zerolog.Logger

	// store is the data-access facade every runtime component reads
	// fleet entities through. The bare *sql.DB stays inside the store
	// package; this type does not hold one.
	store *store.Store

	// daemonCfg is the static daemon-level configuration: HTTP, proxy, log,
	// processor settings. Set at startup and never mutated. The four
	// CRUD-mutable entity sets are NOT held here, they live only in
	// SQLite and are read on demand.
	daemonCfg config.DaemonConfig

	// Runtime collaborators.
	channels  *workflow.DataChannels
	engine    *workflow.Engine
	scheduler *scheduler.Scheduler
	processor *workflow.Processor
	obs       *observe.Store

	// HTTP-side concrete handlers. All non-nil after New returns.
	fleet    *daemonfleet.Handler
	repos    *daemonrepos.Handler
	config   *daemonconfig.Handler
	observe  *daemonobserve.Handler
	runners  *daemonrunners.Handler
	webhook  *webhook.Handler
	delivery *webhook.DeliveryStore
	mcp      http.Handler
	proxy    *anthropicproxy.Handler
	uiFS     fs.FS

	startTime time.Time
}

// New builds the daemon. cfg supplies the static daemon-level fields
// (HTTP, proxy, log, processor), those never mutate via CRUD, so they
// are captured by value at startup. The four CRUD-mutable entity sets
// (agents, repos, skills, backends) live only in the store and are read
// on demand by every runtime component. The caller owns the underlying
// SQLite handle through the store; daemon does not close it.
func New(cfg *config.Config, st *store.Store, logger zerolog.Logger) (*Daemon, error) {
	httpLogger := logger.With().Str("component", "http_server").Logger()

	channels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer, st)
	engine := workflow.NewEngine(st, cfg.Daemon.Processor, channels, logger)

	memBackend := st.NewMemoryBackend()
	engine.WithMemory(memBackend)

	obs := observe.NewStore(st.DB())
	engine.WithTraceRecorder(obs)
	engine.WithGraphRecorder(obs)
	engine.WithRunTracker(obs.ActiveRuns)
	engine.WithStepRecorder(obs)
	engine.WithRunStreamPublisher(obs)
	memBackend.SetChangeNotifier(obs.PublishMemoryChange)

	sched, err := scheduler.NewScheduler(st, scheduler.DefaultReconcileInterval, logger)
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	sched.WithEventQueue(channels)
	engine.WithLastRunRecorder(sched)
	engine.WithBudgetStore(st)

	// Domain handlers (HTTP layer). All take the data-access facade and
	// the static daemon-level config; CRUD-mutable state is read on every
	// request.
	fleetHandler := daemonfleet.New(st, cfg.Daemon.HTTP.MaxBodyBytes, sched, obs, logger)
	reposHandler := daemonrepos.New(st, cfg.Daemon.HTTP.MaxBodyBytes, logger)
	configHandler := daemonconfig.New(st, cfg.Daemon, logger)

	memReader := st.NewMemoryReader()
	observeHandler := daemonobserve.New(obs, st, sched, engine, memReader, logger)
	runnersHandler := daemonrunners.New(st, channels, obs, logger)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.Daemon.HTTP.DeliveryTTLSeconds) * time.Second)
	webhookHandler := webhook.NewHandler(deliveryStore, channels, st, cfg.Daemon.HTTP, logger)

	// Processor sits over the queue; event recorder writes into observe so
	// /events shows the firehose.
	workers := cfg.Daemon.Processor.MaxConcurrentAgents
	shutdown := time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds) * time.Second
	processor := workflow.NewProcessor(channels, engine, workers, shutdown, logger)
	processor.WithEventRecorder(obs)

	d := &Daemon{
		logger:    httpLogger,
		store:     st,
		daemonCfg: cfg.Daemon,
		channels:  channels,
		engine:    engine,
		scheduler: sched,
		processor: processor,
		obs:       obs,
		fleet:     fleetHandler,
		repos:     reposHandler,
		config:    configHandler,
		observe:   observeHandler,
		runners:   runnersHandler,
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

	// MCP last, it consumes every other component. Constructed here so
	// the handler picks up exactly the wiring the REST surface uses.
	d.mcp = mcpserver.New(mcpserver.Deps{
		Store:        st,
		DaemonConfig: cfg.Daemon,
		StatusJSON:   d.StatusJSON,
		Channels:     channels,
		Observe:      obs,
		Engine:       engine,
		Fleet:        fleetHandler,
		Repos:        reposHandler,
		Config:       configHandler,
		RunnersH:     runnersHandler,
		Logger:       logger,
	})

	return d, nil
}

// Store returns the data-access facade. Exported for tests; production
// callers use the domain handlers.
func (d *Daemon) Store() *store.Store { return d.store }

// DaemonConfig returns the static daemon-level configuration, HTTP,
// proxy, log, processor. CRUD-mutable state is NOT here; read it from
// SQLite via the store package.
func (d *Daemon) DaemonConfig() config.DaemonConfig { return d.daemonCfg }

// Engine returns the workflow engine. Exported for tests asserting on
// dispatch counters or active-run state.
func (d *Daemon) Engine() *workflow.Engine { return d.engine }

// Channels returns the daemon's data channels. Exported for tests that
// assert events were enqueued by reading directly off EventChan.
func (d *Daemon) Channels() *workflow.DataChannels { return d.channels }

// Fleet returns the fleet HTTP handler. Exported for tests.
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

// Handler builds and returns the raw HTTP router without daemon auth middleware.
// Exported for handler-level tests that exercise domain behavior directly.
func (d *Daemon) Handler() http.Handler {
	httpCfg := d.daemonCfg.HTTP
	writeTimeout := time.Duration(httpCfg.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}
	return d.buildMuxRouter(withTimeout)
}

// AuthHandler builds and returns the production HTTP handler with daemon auth
// middleware. Exported for tests that need to verify access-control behavior.
func (d *Daemon) AuthHandler() http.Handler { return d.buildRouter() }

// Run starts every long-running goroutine the daemon needs and blocks
// until parentCtx is cancelled. Goroutines are arranged in two tiers
// with separate lifetimes so graceful shutdown is ordered:
//
//   - producers (HTTP listener, cron scheduler) live on a context derived
//     from parentCtx; they stop emitting webhooks and cron events as
//     soon as parentCtx fires or any producer returns an error;
//   - consumers (worker pool, delivery dedup eviction, dispatch dedup
//     eviction, event-queue cleanup) live on a separate background
//     context; they keep running after producers stop so workers can
//     finish processing already-claimed events before exiting.
//
// Before producers start, any pending event_queue rows from a previous
// run are replayed onto the channel so events that were buffered when
// the daemon stopped, or whose runs were interrupted mid-prompt, get
// a second chance.
//
// Sequence on shutdown:
//  1. parentCtx cancelled (SIGTERM) or producer returns error.
//  2. Producer ctx cancels; HTTP server is gracefully drained, scheduler
//     stops cron and reconciler.
//  3. Producer goroutines join.
//  4. Consumer ctx cancels; processor closes the queue and waits for
//     in-flight runs (bounded by shutdown_timeout_seconds); dedup
//     eviction loops and event-queue cleanup exit.
//  5. Consumer goroutines join.
//
// Each phase is logged so an operator reading logs sees the full
// lifecycle.
func (d *Daemon) Run(parentCtx context.Context) error {
	log := d.logger
	log.Info().Msg("starting agents daemon")

	// ── consumer tier ───────────────────────────────────────────
	// Lives on its own background ctx so it outlives the producer tier
	// during shutdown; that's how the queue drains after producers stop.
	consumerCtx, stopConsumers := context.WithCancel(context.Background())
	defer stopConsumers()
	consumers, _ := errgroup.WithContext(consumerCtx)
	consumers.Go(func() error { return d.delivery.Run(consumerCtx) })
	consumers.Go(func() error { return d.engine.RunDispatchDedup(consumerCtx) })
	consumers.Go(func() error { return d.processor.Run(consumerCtx) })
	consumers.Go(func() error {
		return d.store.RunQueueCleanup(consumerCtx, queueRetention, queueCleanupInterval, func(err error) {
			log.Warn().Err(err).Msg("event queue cleanup tick failed")
		})
	})
	log.Info().Msg("consumers ready: processor, delivery dedup, dispatch dedup, queue cleanup")

	// Replay any pending events from a previous run before producers start
	// pushing new ones. Pending = completed_at IS NULL, covers events
	// buffered at shutdown plus runs that crashed mid-prompt. The replay
	// goroutine pushes onto the channel concurrently with workers
	// consuming; it blocks naturally if the channel buffer fills.
	consumers.Go(func() error {
		return d.replayPendingEvents(consumerCtx)
	})

	// ── producer tier ───────────────────────────────────────────
	// Derived from parentCtx so SIGTERM cancels it; errgroup will cancel
	// producerCtx if any producer returns a non-nil error so the others
	// wind down cooperatively.
	producers, producerCtx := errgroup.WithContext(parentCtx)
	producers.Go(func() error { return d.scheduler.Run(producerCtx) })
	producers.Go(func() error { return d.runHTTP(producerCtx) })
	log.Info().Str("addr", d.daemonCfg.HTTP.ListenAddr).Msg("producers ready: http listener, scheduler")
	log.Info().Msg("daemon ready")

	// Block until parentCtx fires or a producer fails.
	producerErr := producers.Wait()
	if producerErr != nil && !errors.Is(producerErr, context.Canceled) {
		log.Warn().Err(producerErr).Msg("producer returned an error; beginning shutdown")
	} else {
		log.Info().Msg("shutdown signal received; stopping producers")
	}
	log.Info().Msg("producers stopped: no new webhooks, no new cron ticks")

	// ── drain ─────────────────────────────────────────────────
	queueDepth := d.channels.QueueStats().Buffered
	log.Info().Int("buffered_events", queueDepth).Dur("shutdown_timeout", time.Duration(d.daemonCfg.HTTP.ShutdownTimeoutSeconds)*time.Second).Msg("draining event queue")
	stopConsumers()
	consumerErr := consumers.Wait()
	log.Info().Msg("consumers stopped; queue drained")
	log.Info().Msg("agents daemon stopped")

	if producerErr != nil && !errors.Is(producerErr, context.Canceled) {
		return producerErr
	}
	if consumerErr != nil && !errors.Is(consumerErr, context.Canceled) {
		return consumerErr
	}
	return nil
}

// replayPendingEvents reads every pending event_queue row from the
// previous run and pushes it onto the in-memory channel for workers.
// Returns nil when ctx is cancelled or the replay completes, either is
// a clean exit. Errors fetching individual rows are logged and the
// replay continues; a row that vanished between scan and fetch is
// already gone, and a malformed blob is best-effort dropped.
func (d *Daemon) replayPendingEvents(ctx context.Context) error {
	ids, err := d.store.PendingEventIDs()
	if err != nil {
		return fmt.Errorf("event queue replay: scan: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	d.logger.Info().Int("pending_events", len(ids)).Msg("replaying pending events from previous run")
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		blob, err := d.store.ReadQueuedEvent(id)
		if err != nil {
			if errors.Is(err, store.ErrRunnerNotFound) {
				continue
			}
			d.logger.Warn().Err(err).Int64("event_id", id).Msg("replay: read event failed")
			continue
		}
		var ev workflow.Event
		if err := json.Unmarshal([]byte(blob), &ev); err != nil {
			d.logger.Warn().Err(err).Int64("event_id", id).Msg("replay: unmarshal event failed")
			continue
		}
		if err := d.channels.ReplayQueued(ctx, workflow.QueuedEvent{ID: id, Event: ev}); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, workflow.ErrQueueClosed) {
				return nil
			}
			d.logger.Warn().Err(err).Int64("event_id", id).Msg("replay: push failed")
		}
	}
	d.logger.Info().Msg("replay complete")
	return nil
}

// queueRetention is how long completed event_queue rows are kept around
// before the cleanup loop removes them. A week is enough to debug recent
// activity (the row carries the original event blob, useful for tracing
// "did this issue ever fire an agent?") without growing the table
// indefinitely on a long-running daemon.
const queueRetention = 7 * 24 * time.Hour

// queueCleanupInterval controls how often the cleanup loop ticks. Hourly
// is plenty, the table is bounded by retention regardless of cadence,
// the cadence just controls the deletion granularity.
const queueCleanupInterval = time.Hour

func (d *Daemon) runHTTP(ctx context.Context) error {
	httpCfg := d.daemonCfg.HTTP
	router := d.buildRouter()

	srv := &http.Server{
		Addr:         httpCfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(httpCfg.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(httpCfg.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:  time.Duration(httpCfg.IdleTimeoutSeconds) * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(httpCfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		errCh <- srv.Shutdown(shutdownCtx)
	}()

	logEvent := d.logger.Info().Str("addr", httpCfg.ListenAddr).Str("status_path", httpCfg.StatusPath).Str("webhook_path", httpCfg.WebhookPath)
	if d.proxy != nil {
		logEvent = logEvent.Str("proxy_path", d.daemonCfg.Proxy.Path)
	}
	logEvent.Msg("starting webhook server")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-errCh
}

func (d *Daemon) buildRouter() http.Handler {
	httpCfg := d.daemonCfg.HTTP

	writeTimeout := time.Duration(httpCfg.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}
	return d.withBearerAuth(d.buildMuxRouter(withTimeout))
}

func (d *Daemon) buildMuxRouter(withTimeout func(http.Handler) http.Handler) *mux.Router {
	httpCfg := d.daemonCfg.HTTP
	router := mux.NewRouter()
	router.Handle(httpCfg.StatusPath, withTimeout(http.HandlerFunc(d.handleStatus))).Methods(http.MethodGet)
	router.Handle("/run", withTimeout(http.HandlerFunc(d.handleAgentsRun))).Methods(http.MethodPost)
	d.registerAuthRoutes(router, withTimeout)
	d.webhook.RegisterRoutes(router, withTimeout)

	router.Handle("/agents", withTimeout(http.HandlerFunc(d.fleet.HandleAgentsView))).Methods(http.MethodGet)
	router.Handle("/agents", withTimeout(http.HandlerFunc(d.fleet.HandleAgentsCreate))).Methods(http.MethodPost)

	d.fleet.RegisterRoutes(router, withTimeout)
	d.repos.RegisterRoutes(router, withTimeout)
	d.config.RegisterRoutes(router, withTimeout)
	d.observe.RegisterRoutes(router, withTimeout)
	d.runners.RegisterRoutes(router, withTimeout)

	if d.uiFS != nil {
		if sub, err := fs.Sub(d.uiFS, "dist"); err == nil {
			fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
			router.PathPrefix("/ui/").Handler(withTimeout(fileServer))
			router.Handle("/ui", withTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			}))).Methods(http.MethodGet)
			router.Handle("/", withTimeout(http.HandlerFunc(d.handleRootLogin))).Methods(http.MethodGet)
		}
	}

	if d.proxy != nil {
		// Proxy enforces its own upstream timeout via http.Client; wrapping
		// with http.TimeoutHandler would impose a hard cap shorter than the
		// configured LLM inference timeout and break long completions.
		router.Handle(d.daemonCfg.Proxy.Path, d.proxy).Methods(http.MethodPost)
		router.Handle("/v1/models", withTimeout(http.HandlerFunc(d.proxy.ModelsHandler))).Methods(http.MethodGet)
		d.logger.Info().Str("path", d.daemonCfg.Proxy.Path).Str("upstream", d.daemonCfg.Proxy.Upstream.URL).Msg("anthropic proxy enabled")
	}
	if d.mcp != nil {
		// MCP streamable-HTTP can hold SSE streams open; do not wrap in
		// http.TimeoutHandler. Per-tool work is bounded inside internal/mcp.
		router.PathPrefix("/mcp").Handler(d.mcp)
		d.logger.Info().Str("path", "/mcp").Msg("mcp server enabled")
	}
	return router
}

// Router returns the raw mux router before auth middleware wrapping.
// Exported for route registration tests.
func (d *Daemon) Router() *mux.Router {
	httpCfg := d.daemonCfg.HTTP
	writeTimeout := time.Duration(httpCfg.WriteTimeoutSeconds) * time.Second
	withTimeout := func(h http.Handler) http.Handler {
		if writeTimeout <= 0 {
			return h
		}
		return http.TimeoutHandler(h, writeTimeout, "handler timed out")
	}
	return d.buildMuxRouter(withTimeout)
}

// ── /status ─────────────────────────────────────────────────────────────

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

// buildStatus assembles the /status payload. Same struct backs the MCP
// get_status tool so both surfaces share one source of truth.
func (d *Daemon) buildStatus() statusJSON {
	q := d.channels.QueueStats()

	resp := statusJSON{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(d.startTime).Seconds()),
		Queues: map[string]statusQueueJSON{
			"events": {Buffered: q.Buffered, Capacity: q.Capacity},
		},
		Agents: slices.Clone(d.scheduler.AgentStatuses()),
	}

	orphanedAgents, err := d.fleet.OrphanedAgents()
	if err != nil {
		d.logger.Warn().Err(err).Msg("status: orphan computation failed")
	}
	resp.OrphanedAgents = statusOrphanSummaryJSON{Count: len(orphanedAgents)}

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

// ── /run ─────────────────────────────────────────────────────────────────

type agentsRunRequest struct {
	Agent string `json:"agent"`
	Repo  string `json:"repo"`
}

func (d *Daemon) handleAgentsRun(w http.ResponseWriter, r *http.Request) {
	var req agentsRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, d.daemonCfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Agent == "" || req.Repo == "" {
		http.Error(w, "agent and repo fields are required", http.StatusBadRequest)
		return
	}

	repos, err := d.store.ReadRepos()
	if err != nil {
		d.logger.Error().Err(err).Msg("/run: read repos")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	want := fleet.NormalizeRepoName(req.Repo)
	idx := slices.IndexFunc(repos, func(r fleet.Repo) bool { return r.Name == want })
	if idx < 0 || !repos[idx].Enabled {
		http.Error(w, "repo not found or disabled", http.StatusNotFound)
		return
	}
	repo := repos[idx]

	ev := workflow.Event{
		ID:    workflow.GenEventID(),
		Repo:  workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
		Kind:  "agents.run",
		Actor: "human",
		Payload: map[string]any{
			"target_agent": req.Agent,
		},
	}

	if _, err := d.channels.PushEvent(r.Context(), ev); err != nil {
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

// LoadConfig is the daemon's startup config-load helper. It opens the
// SQLite database, optionally imports a YAML file into it, runs auto-
// discovery for AI backends if none are configured, and returns the
// validated *config.Config plus the data-access store. The caller owns
// and closes the store. Status / import progress messages are written
// to msg (typically os.Stderr); pass nil to silence them.
//
// An empty database boots cleanly with built-in defaults, no YAML
// import is required. Operators configure the fleet through the
// dashboard / REST / MCP after the daemon is up.
func LoadConfig(ctx context.Context, dbPath, importPath string, msg io.Writer) (*config.Config, *store.Store, error) {
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	st := store.New(db)
	if importPath != "" {
		yamlCfg, err := config.Load(importPath)
		if err != nil {
			st.Close()
			return nil, nil, fmt.Errorf("import: load YAML: %w", err)
		}
		if err := st.Import(yamlCfg); err != nil {
			st.Close()
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
	autoDiscovered, _, err := backends.AutoDiscoverIfBackendsMissing(ctx, st)
	if err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("auto-discover backends: %w", err)
	}
	if autoDiscovered && msg != nil {
		fmt.Fprintln(msg, "startup: discovered AI backends from local CLI tools")
	}
	cfg, err := st.LoadAndValidate()
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return cfg, st, nil
}
