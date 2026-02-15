package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/github"
	"github.com/eloylp/agents/internal/logging"
	"github.com/eloylp/agents/internal/webhook"
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

	githubClient := github.NewClient(cfg.GitHub, logger)
	runners := make(map[string]ai.Runner, len(cfg.AIBackends))
	for name, backendCfg := range cfg.AIBackends {
		runners[name] = ai.NewCommandRunner(
			name,
			backendCfg.Mode,
			backendCfg.Command,
			backendCfg.Args,
			backendCfg.TimeoutSeconds,
			backendCfg.MaxPromptChars,
			backendCfg.RedactionSaltEnv,
			logger.With().Str("component", "ai_runner").Str("agent", name).Logger(),
		)
	}
	engine := workflow.NewEngine(cfg, nil, githubClient, runners, logger)
	webhookServer := webhook.NewServer(
		cfg,
		engine,
		webhook.NewDeliveryStore(time.Duration(cfg.HTTP.DeliveryTTLSeconds)*time.Second),
		logger,
	)

	logger.Info().Msg("agent daemon started")
	if err := webhookServer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal().Err(err).Msg("webhook server stopped with error")
	}
	logger.Info().Msg("agent daemon stopped")
}
