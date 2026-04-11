package autonomous

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

// AgentStatus is the runtime state of a single registered autonomous agent.
type AgentStatus struct {
	Name       string
	Repo       string
	LastRun    *time.Time // nil if the agent has never run in this process lifetime
	NextRun    time.Time
	LastStatus string // "success", "error", or "" if never run
}

// agentEntry records the metadata for a registered cron job.
type agentEntry struct {
	name   string
	repo   string
	cronID cron.EntryID
}

// lastRunRecord holds the outcome of the most recent agent execution.
type lastRunRecord struct {
	at     time.Time
	status string
}

type Scheduler struct {
	cfg          *config.Config
	runners      map[string]ai.Runner
	prompts      *ai.PromptStore
	memories     *MemoryStore
	cron         *cron.Cron
	logger       zerolog.Logger
	ctxMu        sync.RWMutex
	runCtx       context.Context
	agentEntries []agentEntry
	lastRunsMu   sync.RWMutex
	lastRuns     map[string]lastRunRecord // key: "name\x00repo"
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
		lastRuns: make(map[string]lastRunRecord),
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
			s.agentEntries = append(s.agentEntries, agentEntry{name: agent.Name, repo: repo.FullName, cronID: id})
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
		runStatus := "success"
		if err != nil {
			logger.Error().Err(err).Msg("autonomous agent run completed with errors")
			runStatus = "error"
		}
		s.recordLastRun(agent.Name, repo, time.Now(), runStatus)
	}
}

func (s *Scheduler) recordLastRun(name, repo string, at time.Time, status string) {
	key := name + "\x00" + repo
	s.lastRunsMu.Lock()
	s.lastRuns[key] = lastRunRecord{at: at, status: status}
	s.lastRunsMu.Unlock()
}

// AgentStatuses returns the current scheduling state for all registered agents.
func (s *Scheduler) AgentStatuses() []AgentStatus {
	s.lastRunsMu.RLock()
	runs := make(map[string]lastRunRecord, len(s.lastRuns))
	for k, v := range s.lastRuns {
		runs[k] = v
	}
	s.lastRunsMu.RUnlock()

	entries := s.cron.Entries()
	entryByID := make(map[cron.EntryID]cron.Entry, len(entries))
	for _, e := range entries {
		entryByID[e.ID] = e
	}

	statuses := make([]AgentStatus, 0, len(s.agentEntries))
	for _, ae := range s.agentEntries {
		entry, ok := entryByID[ae.cronID]
		if !ok {
			continue
		}
		key := ae.name + "\x00" + ae.repo
		lr := runs[key]
		var lastRun *time.Time
		if !lr.at.IsZero() {
			t := lr.at
			lastRun = &t
		}
		nextRun := entry.Next
		if nextRun.IsZero() && entry.Schedule != nil {
			nextRun = entry.Schedule.Next(time.Now())
		}
		statuses = append(statuses, AgentStatus{
			Name:       ae.name,
			Repo:       ae.repo,
			LastRun:    lastRun,
			NextRun:    nextRun,
			LastStatus: lr.status,
		})
	}
	return statuses
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
