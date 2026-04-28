package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/logging"
	mcpserver "github.com/eloylp/agents/internal/mcp"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/server"
	serverconfig "github.com/eloylp/agents/internal/server/config"
	serverfleet "github.com/eloylp/agents/internal/server/fleet"
	serverobserve "github.com/eloylp/agents/internal/server/observe"
	serverrepos "github.com/eloylp/agents/internal/server/repos"
	"github.com/eloylp/agents/internal/setup"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/ui"
	"github.com/eloylp/agents/internal/webhook"
	"github.com/eloylp/agents/internal/workflow"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Handle the "setup" subcommand before loading any config — it must be
	// usable before one exists.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		dryRun := len(os.Args) > 2 && os.Args[2] == "--dry-run"
		return setup.Run(setup.NewCommandRunner(), dryRun, os.Stdin, os.Stdout, os.Stderr)
	}

	_ = godotenv.Load()

	dbPath := flag.String("db", "agents.db", "path to SQLite database file")
	importPath := flag.String("import", "", "YAML config file to import into the database")
	flag.Parse()

	cfg, db, err := loadConfig(*dbPath, *importPath)
	if err != nil {
		return err
	}
	defer db.Close()

	logger := logging.NewLogger(cfg.Daemon.Log)

	runners := setupRunners(cfg, logger)
	sched, err := scheduler.NewScheduler(cfg, logger)
	if err != nil {
		return err
	}

	logger.Info().Msg("starting agents daemon")

	memBackend := &sqliteMemory{db: db}

	dataChannels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer)
	engine := workflow.NewEngine(cfg, runners, dataChannels, logger)
	engine.WithMemory(memBackend)

	obs := observe.NewStore(db)
	engine.WithTraceRecorder(obs)
	engine.WithGraphRecorder(obs)
	engine.WithRunTracker(obs.ActiveRuns)
	engine.WithStepRecorder(obs)

	// Cron ticks flow through the event queue: the scheduler pushes a "cron"
	// event and the engine handles execution uniformly with all other event
	// kinds (transcript steps, run-tracker, queue-wait time, dispatch dedup,
	// run-lock — all in one place). The engine notifies the scheduler back
	// via LastRunRecorder so the per-binding schedule view in /agents stays
	// current.
	sched.WithEventQueue(dataChannels)
	engine.WithLastRunRecorder(sched)

	// Hot-reload coordinator: on every CRUD-triggered config change, rebuild
	// runners + propagate to engine + dispatcher + scheduler in lockstep.
	// The Reloader is what server.WithStore receives; from server's view it
	// satisfies the same CronReloader contract scheduler used to provide,
	// but it now coordinates all four runtime components.
	reloader := &daemonReloader{
		cfg:           cfg,
		engine:        engine,
		scheduler:     sched,
		runnerBuilder: makeRunnerBuilder(logger),
		logger:        logger,
	}
	shutdown := time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds) * time.Second
	workers := cfg.Daemon.Processor.MaxConcurrentAgents
	processor := workflow.NewProcessor(dataChannels, engine, workers, shutdown, logger)
	processor.WithEventRecorder(obs)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.Daemon.HTTP.DeliveryTTLSeconds) * time.Second)
	srv := server.NewServer(cfg, dataChannels, sched, engine, logger)
	srv.WithUI(ui.FS)
	srv.WithStore(db, reloader)
	srv.WithWebhook(webhook.NewHandler(deliveryStore, dataChannels, srv, logger))

	// Construct each domain handler externally and wire it in.
	fleetHandler := serverfleet.New(db, srv, srv, sched, obs, logger)
	fleetHandler.RefreshOrphansFromCfg(cfg)
	srv.WithFleet(
		fleetHandler,
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fleetHandler.HandleAgentsView(w, r)
				return
			}
			fleetHandler.HandleAgentsCreate(w, r)
		},
		fleetOrphansAdapter{fleetHandler},
		fleetHandler.RefreshOrphansFromCfg,
	)

	reposHandler := serverrepos.New(db, srv, srv, logger)
	srv.WithRepos(reposHandler)

	configHandler := serverconfig.New(db, srv, srv, logger)
	srv.WithConfig(configHandler)

	// Wire the memory backend's SSE notifier so the UI stream stays live on
	// every successful write.
	memBackend.notifyFn = obs.PublishMemoryChange
	memReader := &sqliteWebhookReader{db: db}
	srv.WithMemoryReader(memReader)

	// Mount the observability routes via a closure so the central server
	// doesn't import internal/server/observe.
	srv.WithObserveRegister(func(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
		obh := serverobserve.New(obs, srv, sched, obs, engine, memReader)
		obh.RegisterRoutes(r, withTimeout)
	})

	// Mount the MCP server on /mcp so MCP-capable clients (Claude Code,
	// Cursor, Cline) can drive the fleet through the tool surface defined
	// in internal/mcp. The handler shares the daemon's running components
	// directly so MCP tools hit the same code paths as the REST API.
	srv.WithMCP(mcpserver.New(mcpserver.Deps{
		DB:      db,
		Server:  srv,
		Queue:   dataChannels,
		Observe: obs,
		Engine:  engine,
		Fleet:   fleetHandler,
		Repos:   reposHandler,
		Config:  configHandler,
		Logger:  logger,
	}))

	group, groupCtx := errgroup.WithContext(ctx)
	deliveryStore.Start(groupCtx)
	engine.StartDispatchDedup(groupCtx)
	group.Go(func() error { return processor.Run(groupCtx) })
	group.Go(func() error { return sched.Run(groupCtx) })
	group.Go(func() error { return srv.Run(groupCtx) })
	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info().Msg("agents daemon stopped")
	return nil
}

func setupRunners(cfg *config.Config, logger zerolog.Logger) map[string]ai.Runner {
	runners := make(map[string]ai.Runner, len(cfg.Daemon.AIBackends))
	for name, backend := range cfg.Daemon.AIBackends {
		runners[name] = ai.NewCommandRunner(
			name,
			"command",
			backend.Command,
			backendEnvOverrides(backend),
			backend.TimeoutSeconds,
			backend.MaxPromptChars,
			backend.RedactionSaltEnv,
			logger,
		)
	}
	return runners
}

func backendEnvOverrides(b fleet.Backend) map[string]string {
	if b.LocalModelURL == "" {
		return nil
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL": b.LocalModelURL,
	}
}

// makeRunnerBuilder returns a factory that the daemonReloader uses to
// rebuild the runner map after a config change. Backend env overrides
// (ANTHROPIC_BASE_URL for local-model routing) are applied here so the
// reloaded runners match the startup-built ones bit-for-bit.
func makeRunnerBuilder(logger zerolog.Logger) func(name string, b fleet.Backend) ai.Runner {
	return func(name string, b fleet.Backend) ai.Runner {
		return ai.NewCommandRunner(
			name, "command", b.Command, backendEnvOverrides(b),
			b.TimeoutSeconds, b.MaxPromptChars, b.RedactionSaltEnv,
			logger,
		)
	}
}

// daemonReloader is the single hot-reload coordinator that the central HTTP
// server calls after every CRUD-triggered config change. It rebuilds the
// runner map from the new backend definitions, propagates the new cfg +
// runners to the engine (in one combined call so readers never see a
// mismatched pair), refreshes the dispatcher's agent map, and finally tells
// the scheduler to swap its cron entries. Each step is a small,
// independently testable operation on a single component — there is no
// other path that mutates these in production.
//
// Implements server.CronReloader.
type daemonReloader struct {
	cfg           *config.Config
	engine        *workflow.Engine
	scheduler     *scheduler.Scheduler
	runnerBuilder func(name string, b fleet.Backend) ai.Runner
	logger        zerolog.Logger
}

// Reload satisfies server.CronReloader. The order matters: build runners
// first (cheap, pure), then notify the engine (which atomically swaps
// cfg+runners under one lock so concurrent runAgent calls cannot observe a
// mismatched pair), then update the dispatcher's agent map (so dispatch
// allowlists reflect the new agent set immediately), and finally rebuild
// the scheduler's cron entries.
func (r *daemonReloader) Reload(repos []fleet.Repo, agents []fleet.Agent, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	newCfg := *r.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends

	newRunners := make(map[string]ai.Runner, len(backends))
	for name, b := range backends {
		newRunners[name] = r.runnerBuilder(name, b)
	}

	r.engine.UpdateConfigAndRunners(&newCfg, newRunners)
	if d := r.engine.Dispatcher(); d != nil {
		d.UpdateAgents(agents)
	}
	if err := r.scheduler.RebuildCron(repos, agents, skills, backends); err != nil {
		return err
	}
	r.cfg = &newCfg
	return nil
}

// sqliteMemory implements workflow.MemoryBackend using the SQLite store.
// Agent and repo names are normalised with ai.NormalizeToken before storage so
// that the keys are identical to those used by the file-based backend and can
// be looked up by the /api/memory endpoint without conversion.
type sqliteMemory struct {
	db       *sql.DB
	notifyFn func(agent, repo string) // optional; called after each successful write
}

func (m *sqliteMemory) ReadMemory(agent, repo string) (string, error) {
	content, _, _, err := store.ReadMemory(m.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	return content, err
}

// sqliteWebhookReader implements server.MemoryReader using the SQLite store.
// Unlike sqliteMemory (which serves the engine and treats a missing row as
// empty memory), this reader returns server.ErrMemoryNotFound when no row
// exists so that GET /api/memory returns 404 for absent entries while still
// returning 200 with an empty body for intentionally-cleared memory.
// The updated_at timestamp is returned so that handleAPIMemory can set the
// X-Memory-Mtime response header.
type sqliteWebhookReader struct {
	db *sql.DB
}

func (r *sqliteWebhookReader) ReadMemory(agent, repo string) (string, time.Time, error) {
	content, found, mtime, err := store.ReadMemory(r.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	if err != nil {
		return "", time.Time{}, err
	}
	if !found {
		return "", time.Time{}, server.ErrMemoryNotFound
	}
	return content, mtime, nil
}

func (m *sqliteMemory) WriteMemory(agent, repo, content string) error {
	if err := store.WriteMemory(m.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo), content); err != nil {
		return err
	}
	if m.notifyFn != nil {
		m.notifyFn(ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	}
	return nil
}

// loadConfig loads the daemon configuration from a SQLite database. When
// importPath is set, the YAML at importPath is parsed and written into the
// database before loading. The returned *sql.DB is kept open for the daemon
// lifetime so that the CRUD API can write to it. The caller must close it.
func loadConfig(dbPath, importPath string) (*config.Config, *sql.DB, error) {
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
		// Count from the source config so the message reflects exactly what
		// was written, not a potentially stale whole-table count.
		nBindings := 0
		for _, r := range yamlCfg.Repos {
			nBindings += len(r.Use)
		}
		fmt.Fprintf(os.Stderr, "import: imported %d backends, %d skills, %d agents, %d repos, %d bindings\n",
			len(yamlCfg.Daemon.AIBackends), len(yamlCfg.Skills),
			len(yamlCfg.Agents), len(yamlCfg.Repos), nBindings)
	}

	autoDiscovered, _, err := backends.AutoDiscoverIfBackendsMissing(context.Background(), db)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("auto-discover backends: %w", err)
	}
	if autoDiscovered {
		fmt.Fprintln(os.Stderr, "startup: discovered AI backends from local CLI tools")
	}

	cfg, err := store.LoadAndValidate(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return cfg, db, nil
}

// fleetOrphansAdapter bridges serverfleet.Handler's concrete orphan snapshot
// type to the cross-package server.OrphansSnapshot shape /status uses, so
// the webhook server doesn't need to import internal/server/fleet.
type fleetOrphansAdapter struct {
	h *serverfleet.Handler
}

func (a fleetOrphansAdapter) OrphansSnapshot() server.OrphansSnapshot {
	snap := a.h.OrphansSnapshot()
	return server.OrphansSnapshot{GeneratedAt: snap.GeneratedAt, Count: snap.Count}
}

func (a fleetOrphansAdapter) RefreshOrphansFromDB() (server.OrphansSnapshot, error) {
	snap, err := a.h.RefreshOrphansFromDB()
	if err != nil {
		return server.OrphansSnapshot{}, err
	}
	return server.OrphansSnapshot{GeneratedAt: snap.GeneratedAt, Count: snap.Count}, nil
}
