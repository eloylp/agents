package autonomous

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

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
			id, err := s.cron.AddFunc(agent.Cron, s.runAgent(repo.FullName, agent))
			if err != nil {
				return fmt.Errorf("schedule autonomous agent %s for repo %s: %w", agent.Name, repo.FullName, err)
			}
			entry := s.cron.Entry(id)
			s.logger.Info().
				Str("repo", repo.FullName).
				Str("agent", agent.Name).
				Str("cron", agent.Cron).
				Time("next_run", entry.Next).
				Msg("autonomous agent scheduled")
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
		err := s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
			ctx := context.Background()
			issueTask := "Scan all open issues and add one succinct comment per issue only if this agent has not commented before. Avoid duplicate comments."
			if err := s.runTask(ctx, runner, backend, repo, agent, "issues", issueTask, memoryPath, memory, logger); err != nil {
				return err
			}
			codeTask := "Inspect the codebase for improvements. If changes are large or uncertain, open an issue describing them. If changes are small and high-confidence, open a PR directly."
			if !s.cfg.AllowAutonomousPRs {
				codeTask = "Inspect the codebase for improvements. If changes are large or uncertain, open an issue describing them. If changes are small and high-confidence, describe the diff in an issue but do not open a PR."
			}
			return s.runTask(ctx, runner, backend, repo, agent, "code", codeTask, memoryPath, memory, logger)
		})
		if err != nil {
			logger.Error().Err(err).Msg("autonomous agent run completed with errors")
		}
	}
}

func (s *Scheduler) runTask(ctx context.Context, runner ai.Runner, backend string, repo string, agent config.AutonomousAgentConfig, taskType string, taskText string, memoryPath string, memory string, logger zerolog.Logger) error {
	prompt, err := s.prompts.AutonomousPrompt(agent.Name, ai.AutonomousPromptData{
		Repo:        repo,
		AgentName:   agent.Name,
		Description: agent.Description,
		Task:        taskText,
		Memory:      memory,
		MemoryPath:  memoryPath,
	})
	if err != nil {
		return fmt.Errorf("%s task prompt: %w", taskType, err)
	}
	logger.Info().Str("task_type", taskType).Msg("running autonomous pass")
	resp, err := runner.Run(ctx, ai.Request{
		Workflow: fmt.Sprintf("autonomous:%s:%s:%s", backend, agent.Name, taskType),
		Repo:     repo,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("%s task: %w", taskType, err)
	}
	logger.Info().Str("task_type", taskType).Int("artifacts_stored", len(resp.Artifacts)).Msg("autonomous pass completed")
	return nil
}
