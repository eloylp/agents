package scheduler

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// zerologCronLogger adapts zerolog.Logger to the cron.Logger interface
// required by chain wrappers such as SkipIfStillRunning.
type zerologCronLogger struct {
	logger zerolog.Logger
}

func (z zerologCronLogger) Info(msg string, keysAndValues ...any) {
	appendLogKV(z.logger.Info(), keysAndValues).Msg(msg)
}

func (z zerologCronLogger) Error(err error, msg string, keysAndValues ...any) {
	appendLogKV(z.logger.Error().Err(err), keysAndValues).Msg(msg)
}

// appendLogKV attaches key-value pairs to a zerolog event. keysAndValues is a
// flat alternating slice of [key, value, key, value, …]; odd-length tails are
// silently ignored, matching cron.Logger convention.
func appendLogKV(e *zerolog.Event, keysAndValues []any) *zerolog.Event {
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		e = e.Interface(fmt.Sprintf("%v", keysAndValues[i]), keysAndValues[i+1])
	}
	return e
}

// AgentStatus is the runtime state of a single registered cron binding.
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
// robfig/cron engine. It is a pure event producer: every cron tick pushes a
// "cron" event onto the queue, and the engine handles execution uniformly
// with all other event kinds. The engine notifies us back via RecordLastRun
// so the per-binding schedule view in /agents stays current.
//
// The scheduler intentionally knows nothing about runners, the dispatcher,
// or memory — those live on the engine. Hot-reload is coordinated by the
// composing daemon (cmd/agents): on config change, the daemon rebuilds
// runners, calls Engine.UpdateConfigAndRunners + Dispatcher.UpdateAgents,
// and finally calls Scheduler.RebuildCron. The scheduler's only job in
// that flow is to swap out cron entries.
type Scheduler struct {
	cfg          *config.Config
	cron         *cron.Cron
	logger       zerolog.Logger
	ctxMu        sync.RWMutex
	runCtx       context.Context
	bindMu       sync.RWMutex // protects cfg and agentEntries during RebuildCron
	agentEntries []agentEntry
	lastRunsMu   sync.RWMutex
	lastRuns     map[string]lastRunRecord // key: "name\x00repo"
	queue        *workflow.DataChannels   // required at runtime; cron ticks push events here for the engine to handle
}

// WithEventQueue wires the engine's event queue. Every cron tick builds a
// "cron" event and pushes it here; the engine handles execution uniformly
// with all other event kinds. The engine's LastRunRecorder hook calls
// RecordLastRun back into this scheduler when the run completes, keeping
// the per-binding schedule view in /agents up to date.
func (s *Scheduler) WithEventQueue(q *workflow.DataChannels) {
	s.queue = q
}

// RecordLastRun is called by the engine after every cron run completes.
// Implements workflow.LastRunRecorder so /agents and /status see the same
// schedule state operators saw under the old in-scheduler execution path.
func (s *Scheduler) RecordLastRun(agent, repo string, at time.Time, status string) {
	s.recordLastRun(agent, repo, at, status)
}

// NewScheduler builds a scheduler and registers all cron-triggered bindings
// found in cfg.Repos[].Use. The scheduler does not run agents itself — it
// pushes "cron" events onto the queue wired via WithEventQueue, and the
// engine handles execution.
func NewScheduler(cfg *config.Config, logger zerolog.Logger) (*Scheduler, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronLogger := zerologCronLogger{logger: logger.With().Str("component", "scheduler").Logger()}
	c := cron.New(
		cron.WithParser(parser),
		cron.WithChain(cron.SkipIfStillRunning(cronLogger)),
	)
	s := &Scheduler{
		cfg:      cfg,
		cron:     c,
		logger:   logger.With().Str("component", "scheduler").Logger(),
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
		s.logger.Info().Msg("no cron bindings configured")
		return nil
	}
	s.logger.Info().Int("jobs", len(s.cron.Entries())).Msg("starting scheduler")
	s.cron.Start()
	<-ctx.Done()
	stopped := s.cron.Stop()
	<-stopped.Done()
	s.logger.Info().Msg("scheduler stopped")
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
			job := s.makeCronJob(repo.Name, agent.Name)
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
				Msg("cron job registered")
		}
	}
	return nil
}

func (s *Scheduler) makeCronJob(repo string, agentName string) func() {
	return func() {
		ctx := s.currentRunCtx()
		if ctx.Err() != nil {
			return
		}
		if s.queue == nil {
			s.logger.Error().Str("repo", repo).Str("agent", agentName).Msg("cron tick: scheduler has no event queue wired (call WithEventQueue at startup)")
			return
		}
		ev := workflow.Event{
			ID:         workflow.GenEventID(),
			Repo:       workflow.RepoRef{FullName: repo, Enabled: true},
			Kind:       "cron",
			Actor:      agentName,
			Payload:    map[string]any{"target_agent": agentName},
			EnqueuedAt: time.Now(),
		}
		if err := s.queue.PushEvent(ctx, ev); err != nil {
			s.logger.Error().Str("repo", repo).Str("agent", agentName).Err(err).Msg("cron tick: enqueue failed")
			s.recordLastRun(agentName, repo, time.Now(), "error")
		}
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
	runs := maps.Clone(s.lastRuns)
	s.lastRunsMu.RUnlock()

	entries := s.cron.Entries()
	entryByID := make(map[cron.EntryID]cron.Entry, len(entries))
	for _, e := range entries {
		entryByID[e.ID] = e
	}

	s.bindMu.RLock()
	agentEntries := make([]agentEntry, len(s.agentEntries))
	copy(agentEntries, s.agentEntries)
	s.bindMu.RUnlock()

	statuses := make([]AgentStatus, 0, len(agentEntries))
	for _, ae := range agentEntries {
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

// RebuildCron replaces the set of registered cron bindings with the entries
// derived from the supplied repos + agents snapshot. It is safe to call while
// the scheduler is running.
//
// The swap is atomic from the scheduler's perspective: new cron entries are
// registered first; only if all registrations succeed are the old entries
// removed and the cfg pointer updated. If registration fails, the old entries
// remain active and the previous cfg snapshot is preserved, so the scheduler
// stays in a consistent state and the caller receives an error.
//
// This method does NOT touch runners, the dispatcher, or any external sink.
// Those concerns belong to the engine; the composing daemon's Reloader
// updates them in lockstep with the cron rebuild on every config change.
func (s *Scheduler) RebuildCron(repos []fleet.Repo, agents []fleet.Agent, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()

	// Save old state for rollback.
	oldCfg := s.cfg
	oldEntries := make([]agentEntry, len(s.agentEntries))
	copy(oldEntries, s.agentEntries)

	// Copy-on-write the cfg pointer so concurrent readers (AgentStatuses) that
	// already snapshotted the old pointer keep seeing a consistent view until
	// they finish; future snapshots see the new cfg.
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends

	s.cfg = &newCfg
	s.agentEntries = s.agentEntries[:0]

	if err := s.registerJobs(); err != nil {
		// Remove any partially-registered new entries.
		for _, e := range s.agentEntries {
			s.cron.Remove(e.cronID)
		}
		// Restore old state so the scheduler stays healthy.
		s.cfg = oldCfg
		s.agentEntries = oldEntries
		return err
	}

	// New registrations succeeded — remove the old cron entries.
	for _, e := range oldEntries {
		s.cron.Remove(e.cronID)
	}

	s.logger.Info().Int("cron_jobs", len(s.agentEntries)).Msg("scheduler reloaded")
	return nil
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
