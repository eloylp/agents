package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/autonomous"
	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/logging"
	mcpserver "github.com/eloylp/agents/internal/mcp"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/server"
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
	runAgent := flag.String("run-agent", "", "run a single autonomous agent pass and exit (requires --repo)")
	runRepo := flag.String("repo", "", "repo to target when using --run-agent (e.g. owner/repo)")
	flag.Parse()

	cfg, db, err := loadConfig(*dbPath, *importPath)
	if err != nil {
		return err
	}
	defer db.Close()

	logger := logging.NewLogger(cfg.Daemon.Log)

	runners := setupRunners(cfg, logger)
	scheduler, memBackend, err := setupScheduler(cfg, runners, db, logger)
	if err != nil {
		return err
	}
	// Wire the runner factory so that hot-reloaded backend definitions produce
	// live runners without a restart. The same factory is used for the initial
	// setup, so the two paths stay in sync automatically.
	scheduler.WithRunnerBuilder(func(name string, b fleet.Backend) ai.Runner {
		return ai.NewCommandRunner(
			name, "command", b.Command, backendEnvOverrides(b),
			b.TimeoutSeconds, b.MaxPromptChars, b.RedactionSaltEnv,
			logger,
		)
	})

	// --run-agent mode: execute one agent pass synchronously and exit.
	if *runAgent != "" {
		if *runRepo == "" {
			return fmt.Errorf("--repo is required when using --run-agent")
		}
		// Size the buffer to hold every dispatch that could ever be in flight at
		// once.  drainDispatches processes events serially and each handled event
		// can enqueue up to MaxFanout children; at the deepest level the queue can
		// therefore hold MaxFanout^MaxDepth events simultaneously.  Using a linear
		// MaxFanout*MaxDepth estimate is too small for chained/fanout chains and
		// would cause PushEvent to silently drop later hops.
		d := cfg.Daemon.Processor.Dispatch
		runBuf := 1
		for range d.MaxDepth {
			runBuf *= d.MaxFanout
		}
		runBuf = max(runBuf, cfg.Daemon.Processor.EventQueueBuffer)
		dataChannels := workflow.NewDataChannels(runBuf)
		engine := workflow.NewEngine(cfg, runners, dataChannels, logger)
		engine.WithMemory(memBackend)
		scheduler.WithDispatcher(engine.Dispatcher())
		logger.Info().Str("agent", *runAgent).Str("repo", *runRepo).Msg("running autonomous agent on demand")
		engine.StartDispatchDedup(ctx)
		if err := scheduler.TriggerAgent(ctx, *runAgent, *runRepo); err != nil {
			if errors.Is(err, autonomous.ErrDispatchSkipped) {
				logger.Info().Str("agent", *runAgent).Str("repo", *runRepo).Msg("agent run skipped: dispatch already claimed within dedup window")
				return nil
			}
			return fmt.Errorf("run agent: %w", err)
		}
		if err := drainDispatches(ctx, dataChannels, engine); err != nil {
			return fmt.Errorf("drain dispatches: %w", err)
		}
		logger.Info().Str("agent", *runAgent).Str("repo", *runRepo).Msg("on-demand agent run completed")
		return nil
	}

	logger.Info().Msg("starting agents daemon")

	dataChannels := workflow.NewDataChannels(cfg.Daemon.Processor.EventQueueBuffer)
	engine := workflow.NewEngine(cfg, runners, dataChannels, logger)
	engine.WithMemory(memBackend)
	scheduler.WithDispatcher(engine.Dispatcher())
	// Wire the engine as the hot-reload sink so that CRUD-triggered Reload
	// calls propagate new config and runner maps to the event-driven path
	// without a daemon restart.
	scheduler.WithHotReloadSink(engine)
	shutdown := time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds) * time.Second
	workers := cfg.Daemon.Processor.MaxConcurrentAgents
	processor := workflow.NewProcessor(dataChannels, engine, workers, shutdown, logger)

	// Wire the observability store: records events, spans, dispatch graph, and
	// active-run state for the fleet dashboard.
	obs := observe.NewStore(db)
	processor.WithEventRecorder(obs)
	engine.WithTraceRecorder(obs)
	engine.WithGraphRecorder(obs)
	engine.WithRunTracker(obs.ActiveRuns)
	engine.WithStepRecorder(obs)
	scheduler.WithTraceRecorder(obs)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.Daemon.HTTP.DeliveryTTLSeconds) * time.Second)
	srv := webhook.NewServer(cfg, deliveryStore, dataChannels, schedulerStatusAdapter{scheduler}, engine, logger)
	srv.WithUI(ui.FS)
	srv.WithObserve(obs)
	srv.WithRuntimeState(obs)
	srv.WithStore(db, scheduler)

	// Wire the memory backend into the server for the /memory endpoint and
	// attach an SSE notifier so the UI stream stays live.
	mem := memBackend.(*sqliteMemory)
	mem.notifyFn = obs.PublishMemoryChange
	srv.WithMemoryReader(&sqliteWebhookReader{db: db})

	// Mount the MCP server on /mcp so MCP-capable clients (Claude Code,
	// Cursor, Cline) can drive the fleet through the tool surface defined
	// in internal/mcp. The handler shares the daemon's config snapshot,
	// event queue, observability store, dispatch stats, and memory reader
	// so MCP tools stay consistent with the REST API.
	srv.WithMCP(mcpserver.New(mcpserver.Deps{
		DB:            db,
		Config:        srv,
		Queue:         dataChannels,
		Status:        srv,
		Observe:       obs,
		DispatchStats: engine,
		Memory:        &sqliteMcpReader{db: db},
		ConfigBytes:   srv,
		ConfigImport:  srv,
		AgentWrite:    srv,
		SkillWrite:    srv,
		BackendWrite:  srv,
		RepoWrite:     srv.Repos(),
		BindingWrite:  srv.Repos(),
		Logger:        logger,
	}))

	group, groupCtx := errgroup.WithContext(ctx)
	deliveryStore.Start(groupCtx)
	engine.StartDispatchDedup(groupCtx)
	group.Go(func() error { return processor.Run(groupCtx) })
	group.Go(func() error { return scheduler.Run(groupCtx) })
	group.Go(func() error { return srv.Run(groupCtx) })
	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info().Msg("agents daemon stopped")
	return nil
}

// drainDispatches processes all agent.dispatch events that were enqueued during
// a --run-agent pass. Each processed event may itself enqueue further dispatches
// (chained dispatch), so the loop continues until the queue is empty. Any error
// from HandleEvent is returned immediately so the caller can report a failed chain.
func drainDispatches(ctx context.Context, dc *workflow.DataChannels, eng *workflow.Engine) error {
	for {
		select {
		case ev, ok := <-dc.EventChan():
			if !ok {
				return nil
			}
			if err := eng.HandleEvent(ctx, ev); err != nil {
				return fmt.Errorf("kind %s: %w", ev.Kind, err)
			}
		default:
			return nil
		}
	}
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

func setupScheduler(cfg *config.Config, runners map[string]ai.Runner, db *sql.DB, logger zerolog.Logger) (*autonomous.Scheduler, autonomous.MemoryBackend, error) {
	memBackend := &sqliteMemory{db: db}
	sched, err := autonomous.NewScheduler(cfg, runners, memBackend, logger)
	return sched, memBackend, err
}

// sqliteMemory implements autonomous.MemoryBackend using the SQLite store.
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
// Unlike sqliteMemory (which serves the scheduler and treats a missing row as
// empty memory), this reader returns server.ErrMemoryNotFound when no row
// exists so that GET /api/memory returns 404 for absent entries while still
// returning 200 with an empty body for intentionally-cleared memory.
// The updated_at timestamp is returned so that handleAPIMemory can set the
// X-Memory-Mtime response header, keeping file and SQLite mode semantically
// aligned.
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

// sqliteMcpReader implements mcp.MemoryReader. The bool `found` flag keeps
// the MCP "memory not found" signal independent of webhook's sentinel error
// so the two packages don't need to share a sentinel across the import graph.
type sqliteMcpReader struct {
	db *sql.DB
}

func (r *sqliteMcpReader) ReadMemory(agent, repo string) (string, time.Time, bool, error) {
	content, found, mtime, err := store.ReadMemory(r.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	if err != nil {
		return "", time.Time{}, false, err
	}
	return content, mtime, found, nil
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

// schedulerStatusAdapter adapts *autonomous.Scheduler to server.StatusProvider,
// converting autonomous.AgentStatus to server.AgentStatus without coupling
// those packages to each other.
type schedulerStatusAdapter struct {
	s *autonomous.Scheduler
}

func (a schedulerStatusAdapter) AgentStatuses() []server.AgentStatus {
	raw := a.s.AgentStatuses()
	out := make([]server.AgentStatus, len(raw))
	for i, s := range raw {
		out[i] = server.AgentStatus(s)
	}
	return out
}
