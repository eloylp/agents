package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/github"
	"github.com/eloylp/agents/internal/logging"
	"github.com/eloylp/agents/internal/poller"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	// Load .env file if present, ignore error if file doesn't exist.
	_ = godotenv.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}

	logger := logging.NewLogger(cfg.Log)
	zerolog.DefaultContextLogger = &logger
	if cfg.UsedLegacyBackendConfig {
		logger.Warn().Msg("ai_backend is deprecated; migrate to default_agent + agents config")
	}

	storeClient, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to open database")
	}
	defer func() {
		if err := storeClient.Close(); err != nil {
			logger.Error().Err(err).Msg("failed to close database")
		}
	}()

	if cfg.Database.AutoMigrate {
		if err := storeClient.EnsureSchema(ctx); err != nil {
			logger.Fatal().Err(err).Msg("failed to migrate schema")
		}
	}

	for _, repo := range cfg.Repos {
		record := store.RepoRecord{
			FullName:            repo.FullName,
			Enabled:             repo.Enabled,
			PollIntervalSeconds: repo.PollIntervalSeconds,
		}
		if err := storeClient.UpsertRepo(ctx, record); err != nil {
			logger.Fatal().Err(err).Str("repo", repo.FullName).Msg("failed to register repo")
		}
	}

	githubClient := github.NewClient(cfg.GitHub, logger)
	runners := make(map[string]ai.Runner, len(cfg.Agents))
	for name, agentCfg := range cfg.Agents {
		runners[name] = ai.NewCommandRunner(
			name,
			agentCfg.Mode,
			agentCfg.Command,
			agentCfg.Args,
			agentCfg.TimeoutSeconds,
			agentCfg.MaxPromptChars,
			agentCfg.RedactionSaltEnv,
			logger.With().Str("component", "ai_runner").Str("agent", name).Logger(),
		)
	}
	engine := workflow.NewEngine(cfg, storeClient, githubClient, runners, logger)
	poller := poller.New(cfg, storeClient, githubClient, engine, logger)

	logger.Info().Msg("agent daemon started")
	if err := poller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal().Err(err).Msg("poller stopped with error")
	}
	logger.Info().Msg("agent daemon stopped")
}
