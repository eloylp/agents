package autonomous

import (
	"context"
	"fmt"
	"sync"

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
	ctxMu    sync.RWMutex
	runCtx   context.Context
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

func (s *Scheduler) Run(ctx context.Context) error {
	s.setRunCtx(ctx)
	if len(s.cron.Entries()) == 0 {
		s.logger.Info().Msg("no autonomous agents configured")
		return nil
	}
	s.logger.Info().Int("jobs", len(s.cron.Entries())).Msg("starting autonomous scheduler")
	s.cron.Start()
	<-ctx.Done()
	stopped := s.cron.Stop()
	<-stopped.Done()
	s.logger.Info().Msg("autonomous scheduler stopped")
	return nil
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
		ctx := s.currentRunCtx()
		if ctx.Err() != nil {
			return
		}
		logger := s.logger.With().Str("repo", repo).Str("agent", agent.Name).Logger()
		backend := s.resolveBackend(agent.Backend)
		if backend == "" {
			logger.Error().Str("configured_backend", agent.Backend).Msg("no configured backend for autonomous agent run")
			return
		}
		runner, ok := s.runners[backend]
		if !ok {
			logger.Error().Str("backend", backend).Msg("no runner for configured backend")
			return
		}
		err := s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
			for _, task := range agent.Tasks {
				if err := s.runTask(ctx, runner, backend, repo, agent, task.Name, task.Prompt, memoryPath, memory, logger); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			logger.Error().Err(err).Msg("autonomous agent run completed with errors")
		}
	}
}

func (s *Scheduler) resolveBackend(configured string) string {
	configured = ai.NormalizeToken(configured)
	if configured == "" || configured == "auto" {
		return s.cfg.DefaultConfiguredBackend()
	}
	if _, ok := s.cfg.AIBackends[configured]; !ok {
		return ""
	}
	return configured
}

func (s *Scheduler) setRunCtx(ctx context.Context) {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()
	s.runCtx = ctx
}

func (s *Scheduler) currentRunCtx() context.Context {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	if s.runCtx == nil {
		return context.Background()
	}
	return s.runCtx
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
