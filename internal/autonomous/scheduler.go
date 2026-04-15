package autonomous

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// zerologCronLogger adapts zerolog.Logger to the cron.Logger interface
// required by chain wrappers such as SkipIfStillRunning.
type zerologCronLogger struct {
	logger zerolog.Logger
}

func (z zerologCronLogger) Info(msg string, keysAndValues ...interface{}) {
	e := z.logger.Info()
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		e = e.Interface(fmt.Sprintf("%v", keysAndValues[i]), keysAndValues[i+1])
	}
	e.Msg(msg)
}

func (z zerologCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	e := z.logger.Error().Err(err)
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		e = e.Interface(fmt.Sprintf("%v", keysAndValues[i]), keysAndValues[i+1])
	}
	e.Msg(msg)
}

// AgentStatus is the runtime state of a single registered autonomous binding.
type AgentStatus struct {
	Name       string
	Repo       string
	LastRun    *time.Time // nil if never run in this process lifetime
	NextRun    time.Time
	LastStatus string // "success", "error", or "" if never run
}

// agentEntry records the metadata for a registered cron job.
type agentEntry struct {
	name   string
	repo   string
	cronID cron.EntryID
}

// lastRunRecord holds the outcome of the most recent binding execution.
type lastRunRecord struct {
	at     time.Time
	status string
}

// Scheduler wires cron-triggered agent bindings from the config into the
// robfig/cron engine.
type Scheduler struct {
	cfg          *config.Config
	runners      map[string]ai.Runner
	memories     *MemoryStore
	cron         *cron.Cron
	logger       zerolog.Logger
	ctxMu        sync.RWMutex
	runCtx       context.Context
	agentEntries []agentEntry
	lastRunsMu   sync.RWMutex
	lastRuns     map[string]lastRunRecord // key: "name\x00repo"
	dispatcher   *workflow.Dispatcher    // nil when dispatch is not configured
}

// WithDispatcher attaches a Dispatcher to the Scheduler so that dispatch
// requests returned by autonomous agent runs are enqueued and safety-checked
// through the same limits and dedup store used by the event-driven path.
// Call this after creating both the Scheduler and the Engine but before
// starting the Scheduler.
func (s *Scheduler) WithDispatcher(d *workflow.Dispatcher) {
	s.dispatcher = d
}

// NewScheduler builds a scheduler and registers all cron-triggered bindings
// found in cfg.Repos[].Use.
func NewScheduler(cfg *config.Config, runners map[string]ai.Runner, memories *MemoryStore, logger zerolog.Logger) (*Scheduler, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronLogger := zerologCronLogger{logger: logger.With().Str("component", "autonomous_scheduler").Logger()}
	c := cron.New(
		cron.WithParser(parser),
		cron.WithChain(cron.SkipIfStillRunning(cronLogger)),
	)
	s := &Scheduler{
		cfg:      cfg,
		runners:  runners,
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

// Run starts the cron engine and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	s.setRunCtx(ctx)
	if len(s.cron.Entries()) == 0 {
		s.logger.Info().Msg("no autonomous bindings configured")
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
	for _, repo := range s.cfg.Repos {
		if !repo.Enabled {
			continue
		}
		for _, binding := range repo.Use {
			if !binding.IsEnabled() || !binding.IsCron() {
				continue
			}
			agent, ok := s.cfg.AgentByName(binding.Agent)
			if !ok {
				// Validation at config load should have caught this; defensive.
				return fmt.Errorf("binding references unknown agent %q on repo %q", binding.Agent, repo.Name)
			}
			job := s.makeCronJob(repo.Name, agent)
			id, err := s.cron.AddFunc(binding.Cron, job)
			if err != nil {
				return fmt.Errorf("schedule agent %q for repo %q: %w", agent.Name, repo.Name, err)
			}
			s.agentEntries = append(s.agentEntries, agentEntry{name: agent.Name, repo: repo.Name, cronID: id})
			entry := s.cron.Entry(id)
			s.logger.Info().
				Str("repo", repo.Name).
				Str("agent", agent.Name).
				Str("cron", binding.Cron).
				Time("next_run", entry.Next).
				Msg("autonomous agent scheduled")
		}
	}
	return nil
}

func (s *Scheduler) makeCronJob(repo string, agent config.AgentDef) func() {
	return func() {
		ctx := s.currentRunCtx()
		if ctx.Err() != nil {
			return
		}
		status := "success"
		if err := s.executeAgentRun(ctx, repo, agent); err != nil {
			s.logger.Error().Str("repo", repo).Str("agent", agent.Name).Err(err).Msg("autonomous agent run completed with errors")
			status = "error"
		}
		s.recordLastRun(agent.Name, repo, time.Now(), status)
	}
}

func (s *Scheduler) recordLastRun(name, repo string, at time.Time, status string) {
	key := name + "\x00" + repo
	s.lastRunsMu.Lock()
	s.lastRuns[key] = lastRunRecord{at: at, status: status}
	s.lastRunsMu.Unlock()
}

// AgentStatuses returns the current scheduling state for all registered bindings.
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

// TriggerAgent runs the named agent for the given repo synchronously. It
// returns an error if the agent is not found, not bound to the repo, or if
// the run itself fails.
func (s *Scheduler) TriggerAgent(ctx context.Context, agentName, repo string) error {
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	repoDef, ok := s.cfg.RepoByName(repo)
	if !ok || !repoDef.Enabled {
		return fmt.Errorf("repo %q is disabled or not found", repo)
	}
	agent, ok := s.cfg.AgentByName(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found", agentName)
	}
	// The agent must actually be bound to this repo (any trigger kind is
	// sufficient for on-demand execution).
	if !slices.ContainsFunc(repoDef.Use, func(b config.Binding) bool {
		return b.Agent == agentName && b.IsEnabled()
	}) {
		return fmt.Errorf("agent %q is not bound to repo %q", agentName, repo)
	}
	return s.executeAgentRun(ctx, repoDef.Name, agent)
}

func (s *Scheduler) executeAgentRun(ctx context.Context, repo string, agent config.AgentDef) error {
	// Collapse cron-vs-dispatch races: if a dispatch for the same (agent, repo)
	// pair is already in-flight (or vice-versa), skip this run so the two paths
	// do not execute the same agent twice within the dedup window.
	if s.dispatcher != nil {
		if s.dispatcher.CheckAndMarkAutonomousRun(agent.Name, repo, time.Now()) {
			s.logger.Info().Str("repo", repo).Str("agent", agent.Name).
				Msg("autonomous run skipped: already seen within dispatch dedup window")
			return nil
		}
		// We marked the slot; clear it when done so the next scheduled run
		// is not suppressed for the full TTL window.
		defer s.dispatcher.ClearAutonomousRunMark(agent.Name, repo)
	}
	backend := s.cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("no configured backend for agent %q (configured: %q)", agent.Name, agent.Backend)
	}
	runner, ok := s.runners[backend]
	if !ok {
		return fmt.Errorf("no runner for backend %q", backend)
	}
	logger := s.logger.With().Str("repo", repo).Str("agent", agent.Name).Str("backend", backend).Logger()
	roster := s.buildRoster(repo, agent.Name)
	return s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
		prompt, err := ai.RenderAgentPrompt(agent, s.cfg.Skills, ai.PromptContext{
			Repo:       repo,
			Backend:    backend,
			Memory:     memory,
			MemoryPath: memoryPath,
			Roster:     roster,
		})
		if err != nil {
			return fmt.Errorf("render prompt: %w", err)
		}
		if !agent.AllowPRs {
			prompt = "Do not open or create pull requests under any circumstances.\n" + prompt
		}
		logger.Info().Msg("running autonomous pass")
		resp, err := runner.Run(ctx, ai.Request{
			Workflow: fmt.Sprintf("autonomous:%s:%s", backend, agent.Name),
			Repo:     repo,
			Prompt:   prompt,
		})
		if err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Int("dispatch_requests", len(resp.Dispatch)).Msg("autonomous pass completed")
		if s.dispatcher != nil && len(resp.Dispatch) > 0 {
			// Synthesize a minimal event to carry repo context into the dispatcher.
			// Autonomous runs have no originating GitHub event, so Kind="autonomous"
			// and Number=0. If the agent omitted number in a dispatch request, the
			// dispatcher will fall back to this 0.
			// Generate a fresh root event ID so dispatch chains from autonomous runs
			// carry a non-empty correlation ID throughout the chain.
			rootEventID := workflow.GenEventID()
			syntheticEv := workflow.Event{
				ID:    rootEventID,
				Repo:  workflow.RepoRef{FullName: repo, Enabled: true},
				Kind:  "autonomous",
				Actor: agent.Name,
			}
			s.dispatcher.ProcessDispatches(ctx, agent, syntheticEv, rootEventID, 0, resp.Dispatch)
		}
		return nil
	})
}

// buildRoster returns the roster of peer agents for the given repo, excluding
// the current agent.
func (s *Scheduler) buildRoster(repoName, currentAgentName string) []ai.RosterEntry {
	repoDef, ok := s.cfg.RepoByName(repoName)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	var roster []ai.RosterEntry
	for _, b := range repoDef.Use {
		if !b.IsEnabled() || b.Agent == currentAgentName {
			continue
		}
		if _, dup := seen[b.Agent]; dup {
			continue
		}
		agent, ok := s.cfg.AgentByName(b.Agent)
		if !ok {
			continue
		}
		seen[b.Agent] = struct{}{}
		roster = append(roster, ai.RosterEntry{
			Name:          agent.Name,
			Description:   agent.Description,
			Skills:        agent.Skills,
			AllowDispatch: agent.AllowDispatch,
		})
	}
	return roster
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
