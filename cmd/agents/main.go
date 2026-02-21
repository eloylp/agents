package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/logging"
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

	_ = godotenv.Load()

	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := logging.NewLogger(cfg.Log)
	logger.Info().Int("shutdown_timeout_seconds", cfg.HTTP.ShutdownTimeoutSeconds).Msg("starting agents daemon")

	runners := make(map[string]ai.Runner, len(cfg.AIBackends))
	for name, backend := range cfg.AIBackends {
		runners[name] = ai.NewCommandRunner(name, backend.Mode, backend.Command, backend.Args, backend.TimeoutSeconds, backend.MaxPromptChars, backend.RedactionSaltEnv, logger)
	}
	engine := workflow.NewEngine(cfg, runners, logger)

	dataChannels := workflow.NewDataChannels(cfg.HTTP.IssueQueueBuffer, cfg.HTTP.PRQueueBuffer)

	var wg sync.WaitGroup
	processor := workflow.NewProcessor(dataChannels, engine, &wg, logger)
	processor.Start(ctx)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.HTTP.DeliveryTTLSeconds) * time.Second)
	server := webhook.NewServer(cfg, deliveryStore, dataChannels, logger)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx)
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		logger.Info().Msg("shutdown signal received")
		if err := <-serverErr; err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTP.ShutdownTimeoutSeconds)*time.Second)
	defer cancel()

	processor.Stop(shutdownCtx)
	if shutdownCtx.Err() == nil {
		wg.Wait()
	} else {
		logger.Warn().Msg("shutdown timed out waiting for background tasks")
	}

	logger.Info().Msg("agents daemon stopped")
	return nil
}
