package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/autonomous"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/logging"
	"github.com/eloylp/agents/internal/setup"
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

	// Handle the "setup" subcommand before loading any config — it has no
	// dependency on a config file and must be usable before one exists.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		dryRun := len(os.Args) > 2 && os.Args[2] == "--dry-run"
		return setup.Run(setup.NewCommandRunner(), dryRun, os.Stdin, os.Stdout, os.Stderr)
	}

	_ = godotenv.Load()

	configPath := flag.String("config", "config.yaml", "path to config file")
	runAgent := flag.String("run-agent", "", "run a single autonomous agent pass and exit (requires --repo)")
	runRepo := flag.String("repo", "", "repo to target when using --run-agent (e.g. owner/repo)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := logging.NewLogger(cfg.Log)

	promptStore, err := setupPromptStore(cfg, logger)
	if err != nil {
		return err
	}

	runners := setupRunners(cfg, logger)

	scheduler, err := setupScheduler(cfg, runners, promptStore, logger)
	if err != nil {
		return err
	}

	// --run-agent mode: execute one agent pass synchronously and exit.
	if *runAgent != "" {
		if *runRepo == "" {
			return fmt.Errorf("--repo is required when using --run-agent")
		}
		logger.Info().Str("agent", *runAgent).Str("repo", *runRepo).Msg("running autonomous agent on demand")
		if err := scheduler.TriggerAgent(ctx, *runAgent, *runRepo); err != nil {
			return fmt.Errorf("run agent: %w", err)
		}
		logger.Info().Str("agent", *runAgent).Str("repo", *runRepo).Msg("on-demand agent run completed")
		return nil
	}

	logger.Info().Msg("starting agents daemon")

	engine := workflow.NewEngine(cfg, runners, promptStore, logger)
	dataChannels := workflow.NewDataChannels(cfg.Processor.IssueQueueBuffer, cfg.Processor.PRQueueBuffer)
	processor := workflow.NewProcessor(dataChannels, engine, time.Duration(cfg.HTTP.ShutdownTimeoutSeconds)*time.Second, logger)
	deliveryStore := webhook.NewDeliveryStore(time.Duration(cfg.HTTP.DeliveryTTLSeconds) * time.Second)
	server := webhook.NewServer(cfg, deliveryStore, dataChannels, schedulerStatusAdapter{scheduler}, logger, scheduler)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return processor.Run(groupCtx)
	})
	group.Go(func() error {
		return scheduler.Run(groupCtx)
	})
	group.Go(func() error {
		return server.Run(groupCtx)
	})
	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info().Msg("agents daemon stopped")
	return nil
}

func setupPromptStore(cfg *config.Config, logger zerolog.Logger) (*ai.PromptStore, error) {
	skills := resolveSkills(cfg)
	prAgents := collectPRAgentSkills(cfg)
	autoAgents := collectAutonomousAgentSkills(cfg)
	issueBase, prBase, autoBase := resolvePrompts(cfg)

	store, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, autoAgents)
	if err != nil {
		return nil, err
	}
	logger.Info().Str("agents_dir", cfg.AgentsDir).Int("skills", len(skills)).Msg("prompt store initialized")
	return store, nil
}

func setupRunners(cfg *config.Config, logger zerolog.Logger) map[string]ai.Runner {
	runners := make(map[string]ai.Runner, len(cfg.AIBackends))
	for name, backend := range cfg.AIBackends {
		runners[name] = ai.NewCommandRunner(name, backend.Mode, backend.Command, backend.Args, *backend.TimeoutSeconds, *backend.MaxPromptChars, backend.RedactionSaltEnv, logger)
	}
	return runners
}

func setupScheduler(cfg *config.Config, runners map[string]ai.Runner, prompts *ai.PromptStore, logger zerolog.Logger) (*autonomous.Scheduler, error) {
	memoryStore := autonomous.NewMemoryStore(cfg.MemoryDir)
	return autonomous.NewScheduler(cfg, runners, prompts, memoryStore, logger)
}

func resolveSkills(cfg *config.Config) []ai.SkillGuidance {
	skills := make([]ai.SkillGuidance, 0, len(cfg.Skills))
	for _, s := range cfg.Skills {
		sg := ai.SkillGuidance{Name: s.Name, Prompt: s.Prompt}
		if s.PromptFile != "" {
			sg.PromptFile = filepath.Join(cfg.AgentsDir, s.PromptFile)
		}
		skills = append(skills, sg)
	}
	return skills
}

func collectPRAgentSkills(cfg *config.Config) []ai.AgentSkills {
	result := make([]ai.AgentSkills, len(cfg.Agents))
	for i, a := range cfg.Agents {
		result[i] = ai.AgentSkills{Name: a.Name, Skills: a.Skills}
	}
	return result
}

func collectAutonomousAgentSkills(cfg *config.Config) []ai.AgentSkills {
	var result []ai.AgentSkills
	seen := make(map[string]struct{})
	for _, repo := range cfg.AutonomousAgents {
		for _, agent := range repo.Agents {
			if _, ok := seen[agent.Name]; !ok {
				seen[agent.Name] = struct{}{}
				result = append(result, ai.AgentSkills{Name: agent.Name, Skills: agent.Skills})
			}
		}
	}
	return result
}

// schedulerStatusAdapter adapts *autonomous.Scheduler to webhook.StatusProvider,
// converting autonomous.AgentStatus to webhook.AgentStatus without coupling the
// two packages to each other.
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

func resolvePrompts(cfg *config.Config) (issue ai.PromptSource, pr ai.PromptSource, auto ai.PromptSource) {
	resolve := func(src config.PromptSourceConfig) ai.PromptSource {
		if src.Prompt != "" {
			return ai.PromptSource{Prompt: src.Prompt}
		}
		return ai.PromptSource{PromptFile: filepath.Join(cfg.AgentsDir, src.PromptFile)}
	}
	return resolve(cfg.Prompts.IssueRefinement), resolve(cfg.Prompts.PRReview), resolve(cfg.Prompts.Autonomous)
}

