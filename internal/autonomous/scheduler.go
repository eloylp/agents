package autonomous

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type Scheduler struct {
	cfg      *config.Config
	runners  map[string]ai.Runner
	prompts  *ai.PromptStore
	memories *MemoryStore
	cron     *cron.Cron
	logger   zerolog.Logger
}

func NewScheduler(cfg *config.Config, runners map[string]ai.Runner, prompts *ai.PromptStore, memories *MemoryStore, logger zerolog.Logger) (*Scheduler, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	c := cron.New(cron.WithParser(parser))
	s := &Scheduler{
		cfg:      cfg,
		runners:  runners,
		prompts:  prompts,
		memories: memories,
		cron:     c,
		logger:   logger.With().Str("component", "autonomous_scheduler").Logger(),
	}
	if err := s.registerJobs(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Scheduler) Start(ctx context.Context) {
	if len(s.cron.Entries()) == 0 {
		s.logger.Info().Msg("no autonomous agents configured")
		return
	}
	s.logger.Info().Int("jobs", len(s.cron.Entries())).Msg("starting autonomous scheduler")
	s.cron.Start()
	select {
	case <-ctx.Done():
	}
	stopped := s.cron.Stop()
	<-stopped.Done()
	s.logger.Info().Msg("autonomous scheduler stopped")
}

func (s *Scheduler) registerJobs() error {
	for _, repoCfg := range s.cfg.AutonomousAgents {
		if !repoCfg.Enabled {
			continue
		}
		repo, ok := s.cfg.RepoByName(repoCfg.Repo)
		if !ok || !repo.Enabled {
			s.logger.Info().Str("repo", repoCfg.Repo).Msg("autonomous agents skipped, repo disabled or missing")
			continue
		}
		for _, agent := range repoCfg.Agents {
			if _, err := s.cron.AddFunc(agent.Cron, s.runAgent(repo.FullName, agent)); err != nil {
				return fmt.Errorf("schedule autonomous agent %s for repo %s: %w", agent.Name, repo.FullName, err)
			}
			s.logger.Info().Str("repo", repo.FullName).Str("agent", agent.Name).Str("cron", agent.Cron).Msg("autonomous agent scheduled")
		}
	}
	return nil
}

func (s *Scheduler) runAgent(repo string, agent config.AutonomousAgentConfig) func() {
	return func() {
		logger := s.logger.With().Str("repo", repo).Str("agent", agent.Name).Logger()
		backend := s.cfg.DefaultConfiguredBackend()
		if backend == "" {
			logger.Error().Msg("no configured backend for autonomous agent run")
			return
		}
		runner, ok := s.runners[backend]
		if !ok {
			logger.Error().Str("backend", backend).Msg("no runner for configured backend")
			return
		}
		group, ctx := errgroup.WithContext(context.Background())
		group.Go(func() error {
			return s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
				return s.runIssueTask(ctx, runner, backend, repo, agent, memoryPath, memory, logger)
			})
		})
		group.Go(func() error {
			return s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
				return s.runCodeTask(ctx, runner, backend, repo, agent, memoryPath, memory, logger)
			})
		})
		if err := group.Wait(); err != nil {
			logger.Error().Err(err).Msg("autonomous agent run completed with errors")
		}
	}
}

func (s *Scheduler) runIssueTask(ctx context.Context, runner ai.Runner, backend string, repo string, agent config.AutonomousAgentConfig, memoryPath string, memory string, logger zerolog.Logger) error {
	task := "Scan all open issues and add one succinct comment per issue only if this agent has not commented before. Avoid duplicate comments."
	prompt, err := s.prompts.AutonomousPrompt(agent.Name, ai.AutonomousPromptData{
		Repo:        repo,
		AgentName:   agent.Name,
		Description: agent.Description,
		Task:        task,
		Memory:      memory,
		MemoryPath:  memoryPath,
	})
	if err != nil {
		return fmt.Errorf("issue task prompt: %w", err)
	}
	logger.Info().Msg("running autonomous issue pass")
	resp, err := runner.Run(ctx, ai.Request{
		Workflow: fmt.Sprintf("autonomous:%s:%s:issues", backend, agent.Name),
		Repo:     repo,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("issue task: %w", err)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("autonomous issue pass completed")
	return nil
}

func (s *Scheduler) runCodeTask(ctx context.Context, runner ai.Runner, backend string, repo string, agent config.AutonomousAgentConfig, memoryPath string, memory string, logger zerolog.Logger) error {
	task := "Inspect the codebase for improvements. If changes are large or uncertain, open an issue describing them. If changes are small and high-confidence, open a PR directly."
	prompt, err := s.prompts.AutonomousPrompt(agent.Name, ai.AutonomousPromptData{
		Repo:        repo,
		AgentName:   agent.Name,
		Description: agent.Description,
		Task:        task,
		Memory:      memory,
		MemoryPath:  memoryPath,
	})
	if err != nil {
		return fmt.Errorf("code task prompt: %w", err)
	}
	logger.Info().Msg("running autonomous code pass")
	resp, err := runner.Run(ctx, ai.Request{
		Workflow: fmt.Sprintf("autonomous:%s:%s:code", backend, agent.Name),
		Repo:     repo,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("code task: %w", err)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("autonomous code pass completed")
	return nil
}
