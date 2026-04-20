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
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/logging"
	"github.com/eloylp/agents/internal/observe"
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

	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "", "path to SQLite database file (alternative to --config)")
	importPath := flag.String("import", "", "YAML config file to import into the database (requires --db)")
	runAgent := flag.String("run-agent", "", "run a single autonomous agent pass and exit (requires --repo)")
	runRepo := flag.String("repo", "", "repo to target when using --run-agent (e.g. owner/repo)")
	flag.Parse()

	cfg, db, err := loadConfig(*configPath, *dbPath, *importPath)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
	}

	logger := logging.NewLogger(cfg.Daemon.Log)

	runners := setupRunners(cfg, logger)
	scheduler, err := setupScheduler(cfg, runners, logger)
	if err != nil {
		return err
	}
	if db != nil {
		scheduler.WithDB(db)
	}

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
	scheduler.WithDispatcher(engine.Dispatcher())
	shutdown := time.Duration(cfg.Daemon.HTTP.ShutdownTimeoutSeconds) * time.Second
	workers := cfg.Daemon.Processor.MaxConcurrentAgents
	processor := workflow.NewProcessor(dataChannels, engine, workers, shutdown, logger)

	// Wire the observability store: records events, spans, dispatch graph, and
	// active-run state for the fleet dashboard.
	obs := observe.NewStore()
	processor.WithEventRecorder(obs)
	engine.WithTraceRecorder(obs)
	engine.WithGraphRecorder(obs)
	engine.WithRunTracker(obs.ActiveRuns)
	scheduler.WithTraceRecorder(obs)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.Daemon.HTTP.DeliveryTTLSeconds) * time.Second)
	server := webhook.NewServer(cfg, deliveryStore, dataChannels, schedulerStatusAdapter{scheduler}, engine, logger)
	server.WithUI(ui.FS)
	server.WithObserve(obs)
	server.WithRuntimeState(obs)

	group, groupCtx := errgroup.WithContext(ctx)
	deliveryStore.Start(groupCtx)
	engine.StartDispatchDedup(groupCtx)
	go observe.WatchMemoryDir(groupCtx, cfg.Daemon.MemoryDir, 0, obs.MemorySSE)
	group.Go(func() error { return processor.Run(groupCtx) })
	group.Go(func() error { return scheduler.Run(groupCtx) })
	group.Go(func() error { return server.Run(groupCtx) })
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
			backend.Args,
			backend.Env,
			backend.TimeoutSeconds,
			backend.MaxPromptChars,
			backend.RedactionSaltEnv,
			logger,
		)
	}
	return runners
}

func setupScheduler(cfg *config.Config, runners map[string]ai.Runner, logger zerolog.Logger) (*autonomous.Scheduler, error) {
	memoryStore := autonomous.NewMemoryStore(cfg.Daemon.MemoryDir)
	return autonomous.NewScheduler(cfg, runners, memoryStore, logger)
}

// loadConfig loads the daemon configuration either from a YAML file (legacy
// path) or from a SQLite database. When dbPath is set:
//   - If importPath is also set, the YAML at importPath is parsed and written
//     into the database before loading.
//   - The config is then read from the database.
//   - The open *sql.DB is returned so the caller can pass it to subsystems
//     (e.g. the scheduler for SQLite-backed memory). The caller owns the DB
//     and must close it when done.
//
// When dbPath is empty, configPath is used (default behaviour) and the
// returned *sql.DB is nil.
func loadConfig(configPath, dbPath, importPath string) (*config.Config, *sql.DB, error) {
	if importPath != "" && dbPath == "" {
		return nil, nil, fmt.Errorf("--import requires --db")
	}
	if dbPath == "" {
		cfg, err := config.Load(configPath)
		return cfg, nil, err
	}
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

	cfg, err := store.LoadAndValidate(db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return cfg, db, nil
}

// schedulerStatusAdapter adapts *autonomous.Scheduler to webhook.StatusProvider,
// converting autonomous.AgentStatus to webhook.AgentStatus without coupling
// those packages to each other.
type schedulerStatusAdapter struct {
	s *autonomous.Scheduler
}

func (a schedulerStatusAdapter) AgentStatuses() []webhook.AgentStatus {
	raw := a.s.AgentStatuses()
	out := make([]webhook.AgentStatus, len(raw))
	for i, s := range raw {
		out[i] = webhook.AgentStatus{
			Name:       s.Name,
			Repo:       s.Repo,
			LastRun:    s.LastRun,
			NextRun:    s.NextRun,
			LastStatus: s.LastStatus,
		}
	}
	return out
}
