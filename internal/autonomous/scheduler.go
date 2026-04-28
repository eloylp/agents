package autonomous

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
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

// RunnerBuilder constructs a new ai.Runner for a given backend name and its
// config. It is used by Reload to keep the in-process runner map consistent
// with SQLite when ai_backend definitions change via the CRUD API.
type RunnerBuilder func(name string, cfg fleet.Backend) ai.Runner


// HotReloadSink is implemented by components that share config and runner
// state with the Scheduler and must be notified when a CRUD-triggered
// Reload replaces those values. Workflow.Engine implements it so that both
// the autonomous and event-driven execution paths stay in sync after a
// hot-reload. Kept as an interface so scheduler_test.go can stub it with
// testHotReloadSink.
type HotReloadSink interface {
	UpdateConfig(cfg *config.Config)
	UpdateRunners(runners map[string]ai.Runner)
	UpdateConfigAndRunners(cfg *config.Config, runners map[string]ai.Runner)
}

// Scheduler wires cron-triggered agent bindings from the config into the
// robfig/cron engine. It is a pure event producer: every cron tick pushes an
// "cron" event onto the queue, and the engine handles execution
// uniformly with all other event kinds. The engine notifies us back via
// RecordLastRun so the per-binding schedule view in /agents stays current.
type Scheduler struct {
	cfg           *config.Config
	runners       map[string]ai.Runner
	runnerBuilder RunnerBuilder // optional; nil means Reload does not update runners
	hotReloadSink HotReloadSink // optional; nil means no external component to notify
	memory        MemoryBackend
	cron          *cron.Cron
	logger        zerolog.Logger
	ctxMu         sync.RWMutex
	runCtx        context.Context
	bindMu        sync.RWMutex // protects cfg, runners, and agentEntries during Reload
	agentEntries  []agentEntry
	lastRunsMu    sync.RWMutex
	lastRuns      map[string]lastRunRecord // key: "name\x00repo"
	dispatcher    *workflow.Dispatcher     // nil when dispatch is not configured
	queue         *workflow.DataChannels   // required at runtime; cron ticks push events here for the engine to handle
}

// WithDispatcher attaches a Dispatcher to the Scheduler so that dispatch
// requests returned by autonomous agent runs are enqueued and safety-checked
// through the same limits and dedup store used by the event-driven path.
// Call this after creating both the Scheduler and the Engine but before
// starting the Scheduler.
func (s *Scheduler) WithDispatcher(d *workflow.Dispatcher) {
	s.dispatcher = d
}

// WithRunnerBuilder registers the factory used to construct ai.Runner instances
// during hot-reload. When set, Reload builds a fresh runners map after a
// successful cron re-registration and updates both the scheduler and any
// registered HotReloadSink so that new or updated backend definitions take
// effect without a daemon restart.
//
// If not called, Reload leaves the runner map untouched; new backends added via
// the CRUD API will return "no runner for backend" until the daemon restarts.
func (s *Scheduler) WithRunnerBuilder(fn RunnerBuilder) {
	s.runnerBuilder = fn
}

// WithHotReloadSink registers a sink (typically *workflow.Engine) that
// shares config and runner state with this scheduler. Updates from
// CRUD-triggered Reloads propagate to the sink so the event-driven path
// stays in sync.
func (s *Scheduler) WithHotReloadSink(sink HotReloadSink) {
	s.hotReloadSink = sink
}

// WithEventQueue switches the scheduler to producer mode: every cron tick
// pushes a "cron" event onto q instead of running synchronously
// via executeAgentRun. Production wires the daemon's workflow.DataChannels
// here so cron runs flow through the engine's normal event-handling path
// (uniform with webhook, dispatch, on-demand). The engine's LastRunRecorder
// hook calls RecordLastRun back into this scheduler when the run completes,
// keeping the per-binding schedule view in /agents up to date.
func (s *Scheduler) WithEventQueue(q *workflow.DataChannels) {
	s.queue = q
}

// RecordLastRun is called by the engine after every autonomous run completes.
// Implements workflow.LastRunRecorder so /agents and /status see the same
// schedule state operators saw under the old in-scheduler execution path.
func (s *Scheduler) RecordLastRun(agent, repo string, at time.Time, status string) {
	s.recordLastRun(agent, repo, at, status)
}

// NewScheduler builds a scheduler and registers all cron-triggered bindings
// found in cfg.Repos[].Use.
func NewScheduler(cfg *config.Config, runners map[string]ai.Runner, memory MemoryBackend, logger zerolog.Logger) (*Scheduler, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronLogger := zerologCronLogger{logger: logger.With().Str("component", "autonomous_scheduler").Logger()}
	c := cron.New(
		cron.WithParser(parser),
		cron.WithChain(cron.SkipIfStillRunning(cronLogger)),
	)
	s := &Scheduler{
		cfg:      cfg,
		runners:  runners,
		memory:   memory,
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
				Msg("autonomous agent scheduled")
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

// Reload replaces the set of registered cron bindings and updates the
// in-memory config so that future runs see consistent agent, repo, skill, and
// backend definitions. It is safe to call while the scheduler is running.
//
// The swap is atomic from the scheduler's perspective: new cron entries are
// registered first; only if all registrations succeed are the old entries
// removed and the config pointer updated. If registration fails, the old
// entries remain active and the old config is preserved, so the scheduler
// stays in a consistent state and the caller receives an error.
func (s *Scheduler) Reload(repos []fleet.Repo, agents []fleet.Agent, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	s.bindMu.Lock()

	// Save old state for rollback.
	oldCfg := s.cfg
	oldRunners := s.runners
	oldEntries := make([]agentEntry, len(s.agentEntries))
	copy(oldEntries, s.agentEntries)

	// Build a new config value so we do not mutate the existing *config.Config
	// in place. Both this scheduler and the workflow engine may hold references
	// to the current pointer; mutating through it would race with concurrent
	// readers in event-driven agent runs. Copy-on-write gives every goroutine
	// that already snapshotted the old pointer a consistent view until it
	// finishes, while future snapshots see the new config.
	//
	// Note: this is a shallow copy. Sub-fields that are value types (structs,
	// scalars) are copied; the four mutable fields (Repos, Agents, Skills,
	// Daemon.AIBackends) are replaced with the caller-supplied slices/maps.
	// Immutable fields such as Daemon.HTTP and Daemon.Log are shared by value
	// and are never written after startup, so sharing is safe.
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends

	// Apply new config and attempt to register new cron jobs.
	s.cfg = &newCfg
	s.agentEntries = s.agentEntries[:0]

	if err := s.registerJobs(); err != nil {
		// Remove any partially-registered new entries.
		for _, e := range s.agentEntries {
			s.cron.Remove(e.cronID)
		}
		// Restore old state so the scheduler stays healthy.
		s.cfg = oldCfg
		s.runners = oldRunners
		s.agentEntries = oldEntries
		s.bindMu.Unlock()
		return err
	}

	// New registrations succeeded — remove the old cron entries.
	for _, e := range oldEntries {
		s.cron.Remove(e.cronID)
	}

	// Keep the dispatcher's agent map in sync so that dispatch allowlist and
	// opt-in checks reflect the new agent definitions immediately.
	if s.dispatcher != nil {
		s.dispatcher.UpdateAgents(agents)
	}

	// Rebuild the runner map to reflect any new or changed backend definitions.
	// We build a fresh map (copy-on-write) rather than mutating the existing
	// one to avoid racing with concurrent executeAgentRun or engine.runAgent
	// calls that may already hold a reference to the old map.
	var sinkRunners map[string]ai.Runner
	if s.runnerBuilder != nil {
		newRunners := make(map[string]ai.Runner, len(backends))
		for name, backend := range backends {
			newRunners[name] = s.runnerBuilder(name, backend)
		}
		s.runners = newRunners
		if s.hotReloadSink != nil {
			sinkRunners = maps.Clone(newRunners)
		}
	}

	// Capture the new config pointer for the sink notification (s.cfg == &newCfg).
	var sinkCfg *config.Config
	if s.hotReloadSink != nil {
		sinkCfg = s.cfg
	}

	s.logger.Info().Int("cron_jobs", len(s.agentEntries)).Msg("scheduler reloaded")

	// Release bindMu BEFORE notifying the engine sink. Engine readers such as
	// runAgent hold cfgMu.RLock() and can call back into TriggerAgent which
	// needs bindMu.RLock(). Calling UpdateConfig/UpdateRunners while still
	// holding bindMu.Lock() creates a cyclic wait (A→cfgMu→bindMu, B→bindMu
	// waiting for A to release). The new config is already committed to s.cfg,
	// so releasing here is safe — a concurrent caller cannot start another
	// Reload because storeMu in the HTTP layer serialises all write+reload paths.
	s.bindMu.Unlock()

	// Notify the external sink (Engine) so that event-driven runs also see the
	// new config and runners without a restart. When both are changing, use the
	// combined method so readers never observe a mismatched config/runner pair.
	if s.hotReloadSink != nil {
		if sinkRunners != nil {
			s.hotReloadSink.UpdateConfigAndRunners(sinkCfg, sinkRunners)
		} else {
			s.hotReloadSink.UpdateConfig(sinkCfg)
		}
	}

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
