package autonomous

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

// ErrDispatchSkipped is returned by executeAgentRun (and propagated through
// TriggerAgent) when the run was skipped because a dispatch has already claimed
// the dedup slot for the same (agent, repo) autonomous context. Callers can
// distinguish this from a real run failure using errors.Is.
var ErrDispatchSkipped = errors.New("autonomous run skipped: dispatch already claimed within dedup window")

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

// RunnerBuilder constructs a new ai.Runner for a given backend name and its
// config. It is used by Reload to keep the in-process runner map consistent
// with SQLite when ai_backend definitions change via the CRUD API.
type RunnerBuilder func(name string, cfg config.AIBackendConfig) ai.Runner

// HotReloadSink is implemented by components that share config and runner
// state with the Scheduler and must be notified when a CRUD-triggered Reload
// replaces those values. Engine implements this interface so that both the
// autonomous and event-driven execution paths stay in sync after a hot-reload.
type HotReloadSink interface {
	// UpdateConfig atomically replaces the config snapshot used for event
	// routing and prompt rendering. It must be safe to call concurrently with
	// ongoing agent runs.
	UpdateConfig(cfg *config.Config)
	// UpdateRunners atomically replaces the runner map. It must be safe to call
	// concurrently with ongoing agent runs.
	UpdateRunners(runners map[string]ai.Runner)
	// UpdateConfigAndRunners atomically replaces both the config snapshot and
	// the runner map in a single critical section. Use this instead of calling
	// UpdateConfig and UpdateRunners separately when both values are changing
	// together so that concurrent readers never observe a mismatched pair.
	UpdateConfigAndRunners(cfg *config.Config, runners map[string]ai.Runner)
}

// Scheduler wires cron-triggered agent bindings from the config into the
// robfig/cron engine.
type Scheduler struct {
	cfg           *config.Config
	runners       map[string]ai.Runner
	runnerBuilder RunnerBuilder  // optional; nil means Reload does not update runners
	hotReloadSink HotReloadSink  // optional; nil means no external component to notify
	memories      *MemoryStore
	cron          *cron.Cron
	logger        zerolog.Logger
	ctxMu         sync.RWMutex
	runCtx        context.Context
	bindMu        sync.RWMutex // protects cfg, runners, and agentEntries during Reload
	agentEntries  []agentEntry
	lastRunsMu    sync.RWMutex
	lastRuns      map[string]lastRunRecord // key: "name\x00repo"
	dispatcher    *workflow.Dispatcher    // nil when dispatch is not configured
	traceRec      workflow.TraceRecorder  // nil when tracing is not configured
}

// WithDispatcher attaches a Dispatcher to the Scheduler so that dispatch
// requests returned by autonomous agent runs are enqueued and safety-checked
// through the same limits and dedup store used by the event-driven path.
// Call this after creating both the Scheduler and the Engine but before
// starting the Scheduler.
func (s *Scheduler) WithDispatcher(d *workflow.Dispatcher) {
	s.dispatcher = d
}

// WithTraceRecorder attaches an optional observer that records a trace span
// for each autonomous agent run (timing, status, artifact count).
func (s *Scheduler) WithTraceRecorder(r workflow.TraceRecorder) {
	s.traceRec = r
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

// WithHotReloadSink registers a component that shares config and runner state
// with this Scheduler. At the end of each successful Reload, the sink's
// UpdateConfig and UpdateRunners methods are called so that the external
// component stays consistent with the new definitions.
func (s *Scheduler) WithHotReloadSink(sink HotReloadSink) {
	s.hotReloadSink = sink
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
		// Snapshot cfg+runners at execution time under the same lock so that
		// the agent definition, backends, and runner set all come from the same
		// config epoch. Resolving the agent by name here (rather than capturing
		// a config.AgentDef by value at registration time) ensures that a cron
		// closure that was dequeued just before a Reload completes will still
		// execute with the post-reload definition once it acquires the read lock.
		s.bindMu.RLock()
		cfg := s.cfg
		runners := s.runners
		s.bindMu.RUnlock()

		agent, ok := cfg.AgentByName(agentName)
		if !ok {
			s.logger.Error().Str("repo", repo).Str("agent", agentName).
				Msg("cron job: agent not found in current config snapshot; skipping run")
			return
		}

		status := "success"
		if err := s.executeAgentRun(ctx, repo, agent, cfg, runners); err != nil {
			switch {
			case errors.Is(err, ErrDispatchSkipped):
				// A dispatch already claimed this slot — the dedup skip is not
				// a failure, but we record "skipped" rather than "success" so
				// that /status does not mislead operators into thinking a real
				// agent pass ran.
				status = "skipped"
			default:
				s.logger.Error().Str("repo", repo).Str("agent", agent.Name).Err(err).Msg("autonomous agent run completed with errors")
				status = "error"
			}
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

// TriggerAgent runs the named agent for the given repo synchronously. It
// returns an error if the agent is not found, not bound to the repo, or if
// the run itself fails.
func (s *Scheduler) TriggerAgent(ctx context.Context, agentName, repo string) error {
	agentName = strings.ToLower(strings.TrimSpace(agentName))

	// Snapshot both cfg and runners under the same read lock so that agent
	// lookup and the subsequent executeAgentRun call share a single consistent
	// epoch and cannot observe a partial hot-reload between the two.
	s.bindMu.RLock()
	cfg := s.cfg
	runners := s.runners
	s.bindMu.RUnlock()

	repoDef, ok := cfg.RepoByName(repo)
	if !ok || !repoDef.Enabled {
		return fmt.Errorf("repo %q is disabled or not found", repo)
	}
	agent, ok := cfg.AgentByName(agentName)
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
	return s.executeAgentRun(ctx, repoDef.Name, agent, cfg, runners)
}

// executeAgentRun runs agent against repo using the cfg and runners snapshot
// provided by the caller. Both must come from the same atomic snapshot (taken
// under bindMu.RLock) so that backend resolution and roster building operate
// on a single consistent epoch without racing against a concurrent Reload.
func (s *Scheduler) executeAgentRun(ctx context.Context, repo string, agent config.AgentDef, cfg *config.Config, runners map[string]ai.Runner) error {
	// Atomically check whether a dispatch has already claimed the
	// (agent, repo, 0) slot and, if not, write the cron mark. This single
	// lock-protected operation eliminates the TOCTOU race where the old split
	// DispatchAlreadyClaimed (read) → MarkAutonomousRun (write) sequence
	// allowed both the cron path and a concurrent dispatch path to observe
	// no opposing claim before either wrote, causing both to proceed.
	if s.dispatcher != nil {
		if !s.dispatcher.TryMarkAutonomousRun(agent.Name, repo, time.Now()) {
			s.logger.Info().Str("repo", repo).Str("agent", agent.Name).
				Msg("autonomous run skipped: dispatch already claimed within dedup window")
			return ErrDispatchSkipped
		}
	}

	// Resolve backend and runner. If either fails, roll back the cron mark
	// written above so that subsequent dispatches are not spuriously suppressed
	// for the full dedup_window_seconds.
	backend := cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		if s.dispatcher != nil {
			s.dispatcher.RollbackAutonomousRun(agent.Name, repo)
		}
		return fmt.Errorf("no configured backend for agent %q (configured: %q)", agent.Name, agent.Backend)
	}
	runner, ok := runners[backend]
	if !ok {
		if s.dispatcher != nil {
			s.dispatcher.RollbackAutonomousRun(agent.Name, repo)
		}
		return fmt.Errorf("no runner for backend %q", backend)
	}

	logger := s.logger.With().Str("repo", repo).Str("agent", agent.Name).Str("backend", backend).Logger()
	roster := workflow.BuildRoster(cfg, repo, agent.Name)
	// runCompleted is set to true once runner.Run returns successfully. The cron
	// mark should only be rolled back when the autonomous pass itself fails
	// (prompt rendering, memory lock, or runner error). A post-run failure such
	// as a dispatch enqueue error must NOT clear the mark — the autonomous pass
	// already committed, so the dedup window must remain in force.
	runCompleted := false
	err := s.memories.WithLock(agent.Name, repo, func(memoryPath string, memory string) error {
		rendered, err := ai.RenderAgentPrompt(agent, cfg.Skills, ai.PromptContext{
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
			rendered.System = "Do not open or create pull requests under any circumstances.\n" + rendered.System
		}
		logger.Info().Msg("running autonomous pass")
		spanStart := time.Now()
		resp, err := runner.Run(ctx, ai.Request{
			Workflow: fmt.Sprintf("autonomous:%s:%s", backend, agent.Name),
			Repo:     repo,
			System:   rendered.System,
			User:     rendered.User,
		})
		spanEnd := time.Now()
		if s.traceRec != nil {
			status, errMsg := "success", ""
			if err != nil {
				status = "error"
				errMsg = err.Error()
			}
			spanID := workflow.GenEventID()
			rootEventID := spanID
			logger.Info().Str("span_id", spanID).Str("status", status).Int64("duration_ms", spanEnd.Sub(spanStart).Milliseconds()).Msg("recording trace span")
			s.traceRec.RecordSpan(
				spanID, rootEventID, "",
				agent.Name, backend,
				repo, "autonomous", "",
				0, 0,
				0, len(resp.Artifacts), resp.Summary,
				spanStart, spanEnd,
				status, errMsg,
			)
		}
		if err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		runCompleted = true
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
			// Autonomous runs do not belong to an inbound trace span so
			// parentSpanID is empty; dispatch children will still create their
			// own spans with this run's rootEventID as correlation.
			if err := s.dispatcher.ProcessDispatches(ctx, agent, syntheticEv, rootEventID, 0, "", resp.Dispatch); err != nil {
				return fmt.Errorf("agent %q: dispatch: %w", agent.Name, err)
			}
		}
		return nil
	})
	if s.dispatcher != nil {
		if err != nil && !runCompleted {
			// Roll back the cron mark only when the autonomous pass itself failed
			// (before or during runner.Run). If runner.Run succeeded but a
			// downstream step (e.g. dispatch enqueue) failed, the run is already
			// committed and the dedup window must stay in force.
			s.dispatcher.RollbackAutonomousRun(agent.Name, repo)
		} else {
			// runner.Run succeeded (runCompleted == true) regardless of whether
			// a post-run step (e.g. dispatch enqueue) failed. Decrement the
			// refcount so the evict() loop can clean up the cron entry after its
			// TTL expires. The entry itself is preserved so that
			// TryClaimForDispatch continues to suppress autonomous-context
			// dispatches for the full dedup_window_seconds window.
			s.dispatcher.FinalizeAutonomousRun(agent.Name, repo)
		}
	}
	return err
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
func (s *Scheduler) Reload(repos []config.RepoDef, agents []config.AgentDef, skills map[string]config.SkillDef, backends map[string]config.AIBackendConfig) error {
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
