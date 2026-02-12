package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/claude"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}

	logger := logging.NewLogger(cfg.Log)
	zerolog.DefaultContextLogger = &logger

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
	runner := claude.NewRunner(cfg.Claude, logger)
	engine := workflow.NewEngine(cfg, storeClient, githubClient, runner, logger)
	poller := poller.New(cfg, storeClient, githubClient, engine, logger)

	logger.Info().Msg("agent daemon started")
	if err := poller.Run(ctx); err != nil && err != context.Canceled {
		logger.Fatal().Err(err).Msg("poller stopped with error")
	}
	logger.Info().Msg("agent daemon stopped")
}
