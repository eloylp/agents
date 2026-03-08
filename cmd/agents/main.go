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
	"github.com/eloylp/agents/internal/autonomous"
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
	logger.Info().Msg("starting agents daemon")

	promptStore, err := ai.NewPromptStore(cfg.AgentsDir)
	if err != nil {
		return err
	}
	prAgents := collectPRAgents(cfg)
	autoAgents := collectAutonomousAgents(cfg)
	if err := promptStore.Validate(prAgents, autoAgents); err != nil {
		return err
	}
	logger.Info().Str("agents_dir", cfg.AgentsDir).Msg("prompt store initialized")

	runners := make(map[string]ai.Runner, len(cfg.AIBackends))
	for name, backend := range cfg.AIBackends {
		runners[name] = ai.NewCommandRunner(name, backend.Mode, backend.Command, backend.Args, backend.TimeoutSeconds, backend.MaxPromptChars, backend.RedactionSaltEnv, logger)
	}
	engine := workflow.NewEngine(cfg, runners, promptStore, logger)

	dataChannels := workflow.NewDataChannels(cfg.Processor.IssueQueueBuffer, cfg.Processor.PRQueueBuffer)

	var wg sync.WaitGroup
	processor := workflow.NewProcessor(dataChannels, engine, &wg, logger)
	processor.Start(ctx)

	memoryStore := autonomous.NewMemoryStore(cfg.AgentsDir)
	scheduler, err := autonomous.NewScheduler(cfg, runners, promptStore, memoryStore, logger)
	if err != nil {
		return err
	}
	go scheduler.Start(ctx)

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.HTTP.DeliveryTTLSeconds) * time.Second)
	server := webhook.NewServer(cfg, deliveryStore, dataChannels, logger)

	if err := server.Run(ctx); err != nil {
		logger.Error().Err(err).Msg("webhook server exited with error")
	}
	logger.Info().Msg("shutdown signal received")

	// A fresh context is required here because the run ctx is already cancelled.
	// The shutdown deadline is used to drain the processor queues.
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

func collectPRAgents(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	for _, backend := range cfg.AIBackends {
		for _, agent := range backend.Agents {
			seen[agent] = struct{}{}
		}
	}
	agents := make([]string, 0, len(seen))
	for a := range seen {
		agents = append(agents, a)
	}
	return agents
}

func collectAutonomousAgents(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	for _, repo := range cfg.AutonomousAgents {
		for _, agent := range repo.Agents {
			seen[agent.Name] = struct{}{}
		}
	}
	agents := make([]string, 0, len(seen))
	for a := range seen {
		agents = append(agents, a)
	}
	return agents
}
