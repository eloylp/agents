package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"

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

	promptStore, err := setupPromptStore(cfg, logger)
	if err != nil {
		return err
	}

	runners := setupRunners(cfg, logger)
	engine := workflow.NewEngine(cfg, runners, promptStore, logger)
	dataChannels := workflow.NewDataChannels(cfg.Processor.IssueQueueBuffer, cfg.Processor.PRQueueBuffer)

	var wg sync.WaitGroup
	processor := workflow.NewProcessor(dataChannels, engine, &wg, logger)
	processor.Start(ctx)

	scheduler, err := setupScheduler(cfg, runners, promptStore, logger)
	if err != nil {
		return err
	}
	var schedulerWG sync.WaitGroup
	schedulerWG.Add(1)
	go func() {
		defer schedulerWG.Done()
		scheduler.Start(ctx)
	}()

	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.HTTP.DeliveryTTLSeconds) * time.Second)
	server := webhook.NewServer(cfg, deliveryStore, dataChannels, logger)

	if err := server.Run(ctx); err != nil {
		logger.Error().Err(err).Msg("webhook server exited with error")
	}

	awaitShutdown(cfg, processor, &wg, &schedulerWG, logger)
	return nil
}

func setupPromptStore(cfg *config.Config, logger zerolog.Logger) (*ai.PromptStore, error) {
	agents := resolveAgents(cfg)
	autoAgentNames := collectAutonomousAgentNames(cfg)
	issueBase, prBase, autoBase := resolvePrompts(cfg)

	store, err := ai.NewPromptStore(issueBase, prBase, autoBase, agents, autoAgentNames)
	if err != nil {
		return nil, err
	}
	logger.Info().Str("agents_dir", cfg.AgentsDir).Int("agents", len(agents)).Msg("prompt store initialized")
	return store, nil
}

func setupRunners(cfg *config.Config, logger zerolog.Logger) map[string]ai.Runner {
	runners := make(map[string]ai.Runner, len(cfg.AIBackends))
	for name, backend := range cfg.AIBackends {
		runners[name] = ai.NewCommandRunner(name, backend.Mode, backend.Command, backend.Args, backend.TimeoutSeconds, backend.MaxPromptChars, backend.RedactionSaltEnv, logger)
	}
	return runners
}

func setupScheduler(cfg *config.Config, runners map[string]ai.Runner, prompts *ai.PromptStore, logger zerolog.Logger) (*autonomous.Scheduler, error) {
	taskPrompts, err := resolveTaskPrompts(cfg)
	if err != nil {
		return nil, err
	}
	memoryStore := autonomous.NewMemoryStore(cfg.MemoryDir)
	return autonomous.NewScheduler(cfg, runners, prompts, taskPrompts, memoryStore, logger)
}

func resolveTaskPrompts(cfg *config.Config) (autonomous.TaskPrompts, error) {
	issueTask, err := cfg.Prompts.AutonomousIssueTask.Resolve(cfg.AgentsDir)
	if err != nil {
		return autonomous.TaskPrompts{}, fmt.Errorf("resolve autonomous issue task prompt: %w", err)
	}
	codeTask, err := cfg.Prompts.AutonomousCodeTask.Resolve(cfg.AgentsDir)
	if err != nil {
		return autonomous.TaskPrompts{}, fmt.Errorf("resolve autonomous code task prompt: %w", err)
	}
	codeTaskNoPRs, err := cfg.Prompts.AutonomousCodeTaskNoPRs.Resolve(cfg.AgentsDir)
	if err != nil {
		return autonomous.TaskPrompts{}, fmt.Errorf("resolve autonomous code task (no PRs) prompt: %w", err)
	}
	return autonomous.TaskPrompts{
		IssueTask:     issueTask,
		CodeTask:      codeTask,
		CodeTaskNoPRs: codeTaskNoPRs,
	}, nil
}

func awaitShutdown(cfg *config.Config, processor *workflow.Processor, wg *sync.WaitGroup, schedulerWG *sync.WaitGroup, logger zerolog.Logger) {
	logger.Info().Msg("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTP.ShutdownTimeoutSeconds)*time.Second)
	defer cancel()

	processor.Stop(shutdownCtx)
	if shutdownCtx.Err() == nil {
		wg.Wait()
	} else {
		logger.Warn().Msg("shutdown timed out waiting for background tasks")
	}
	schedulerWG.Wait()
	logger.Info().Msg("agents daemon stopped")
}

func resolveAgents(cfg *config.Config) []ai.AgentGuidance {
	agents := make([]ai.AgentGuidance, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		ag := ai.AgentGuidance{Name: a.Name, Prompt: a.Prompt}
		if a.PromptFile != "" {
			ag.PromptFile = filepath.Join(cfg.AgentsDir, a.PromptFile)
		}
		agents = append(agents, ag)
	}
	return agents
}

func resolvePrompts(cfg *config.Config) (issue ai.PromptSource, pr ai.PromptSource, auto ai.PromptSource) {
	resolve := func(src config.PromptSourceConfig) ai.PromptSource {
		if src.Prompt != "" {
			return ai.PromptSource{Prompt: src.Prompt}
		}
		return ai.PromptSource{PromptFile: filepath.Join(cfg.AgentsDir, src.PromptFile)}
	}
	return resolve(cfg.Prompts.IssueRefinement), resolve(cfg.Prompts.PRReview), resolve(cfg.Prompts.Autonomous)
}

func collectAutonomousAgentNames(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, repo := range cfg.AutonomousAgents {
		for _, agent := range repo.Agents {
			if _, ok := seen[agent.Name]; !ok {
				seen[agent.Name] = struct{}{}
				names = append(names, agent.Name)
			}
		}
	}
	return names
}
