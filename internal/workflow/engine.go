package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// labeledKinds are the event kinds that trigger label-based bindings.
var labeledKinds = []string{"issues.labeled", "pull_request.labeled"}

// SpanInput is the call shape for TraceRecorder.RecordSpan. Defined
// here as well as in the observe package so the engine doesn't import
// observe just to name the struct; the two are kept structurally
// identical and the observe.Store.RecordSpan adapter accepts this
// shape directly via a thin wrapper at construction time. Keeping the
// type local mirrors how the engine treats every other recorder.
type SpanInput struct {
	SpanID, RootEventID, ParentSpanID string
	Agent, Backend, Repo              string
	EventKind, InvokedBy              string
	Number, DispatchDepth             int
	QueueWaitMs                       int64
	ArtifactsCount                    int
	Summary                           string
	StartedAt, FinishedAt             time.Time
	Status, ErrorMsg                  string
	Prompt                            string
	InputTokens, OutputTokens         int64
	CacheReadTokens, CacheWriteTokens int64
}

// TraceRecorder is an optional observer that the Engine calls when an agent
// run completes. Implementations must be safe for concurrent use.
type TraceRecorder interface {
	RecordSpan(in SpanInput)
}

// RunTracker is an optional observer that the Engine calls when an agent run
// starts and finishes. It is used to report which agents are currently active.
// Implementations must be safe for concurrent use.
type RunTracker interface {
	StartRun(agentName string)
	FinishRun(agentName string)
}

// RunStreamPublisher is an optional collaborator the Engine notifies when a
// run starts and ends. Implementations register the active span on BeginRun
// so the runners view can render an in-flight row with span_id and the UI can
// subscribe to the DB-backed transcript stream. Must be safe for concurrent use.
type RunStreamPublisher interface {
	BeginRun(in BeginRunInput)
	EndRun(spanID string)
}

// BeginRunInput is the call shape for RunStreamPublisher.BeginRun.
type BeginRunInput struct {
	SpanID, EventID, Agent, Backend, Repo, EventKind string
	StartedAt                                        time.Time
}

// GraphRecorder is an optional observer that the Engine calls when a dispatch
// is issued. Implementations must be safe for concurrent use.
type GraphRecorder interface {
	RecordDispatch(from, to, repo string, number int, reason string)
}

// StepRecorder is an optional observer that the Engine calls as an agent run
// produces tool-loop transcript steps. Implementations must be safe for
// concurrent use.
type StepRecorder interface {
	RecordStep(spanID string, step TraceStep)
	RecordSteps(spanID string, steps []TraceStep)
}

// LastRunRecorder is an optional observer that the Engine calls after every
// cron-fired agent run completes. The concrete implementation
// is *scheduler.Scheduler, which updates the lastRuns map that drives the
// per-binding schedule display in the /agents fleet view.
//
// Defined as an interface here purely to break an import cycle: the
// autonomous package already imports workflow (for Event, *Dispatcher,
// TraceRecorder), so workflow cannot import autonomous to reference the
// concrete type. Implementations must be safe for concurrent use.
type LastRunRecorder interface {
	RecordLastRun(workspaceID, agent, repo string, at time.Time, status string)
}

// runLocks serializes concurrent runAgent calls for the same workspace, agent,
// and repo, preventing the lost-update race where two overlapping runs both
// read the same old memory and whichever finishes last silently clobbers the
// other's state. The cron path used to own this serialization; with cron runs
// now flowing through the engine queue alongside webhook/dispatch/run events,
// the engine owns it for every kind.
type runLocks struct {
	m sync.Map // key: string -> *sync.Mutex
}

func (r *runLocks) acquire(key string) {
	v, _ := r.m.LoadOrStore(key, &sync.Mutex{})
	v.(*sync.Mutex).Lock()
}

func (r *runLocks) release(key string) {
	v, ok := r.m.Load(key)
	if ok {
		v.(*sync.Mutex).Unlock()
	}
}

// Engine dispatches workflow events to the agents bound to the target repo.
// It routes each event by matching against label bindings (labels:) for labeled
// events and against event bindings (events:) for all event kinds.
// The special kind "agent.dispatch" bypasses binding lookup and fires the
// target agent named in the payload directly.
// Agent resolution, backend selection, and prompt composition all happen here;
// the runners just execute the resulting prompt.
// MemoryBackend matches scheduler.MemoryBackend so the same SQLite-backed
// implementation can satisfy both surfaces. Defined here as a small local
// interface so the workflow package does not depend on scheduler.
type MemoryBackend interface {
	ReadMemory(agent, repo string) (string, error)
	WriteMemory(agent, repo, content string) error
}

type Engine struct {
	store         *store.Store
	runnerBuilder func(name string, b fleet.Backend) ai.Runner
	dispatcher    *Dispatcher
	memory        MemoryBackend
	maxConcurrent int
	logger        zerolog.Logger
	traceRec      TraceRecorder
	graphRec      GraphRecorder
	runTracker    RunTracker
	streamPub     RunStreamPublisher
	stepRec       StepRecorder
	lastRunRec    LastRunRecorder // optional; only fired for Kind=="cron"
	budgetStore   *store.Store    // optional; gates runs by token budget caps
	runLock       runLocks        // serializes (agent, repo) runs across kinds
	runsDeduped   atomic.Int64
}

// WithMemory attaches a memory backend so the engine can load and persist
// per-(agent, repo) memory across event-driven, dispatched, and on-demand
// runs. When unset, runs proceed without memory regardless of the agent's
// AllowMemory flag.
func (e *Engine) WithMemory(m MemoryBackend) {
	e.memory = m
}

// WithBudgetStore attaches the store used to check token budgets before each
// agent run. When a cap is exceeded the run is rejected before the runner is
// constructed.
func (e *Engine) WithBudgetStore(st *store.Store) {
	e.budgetStore = st
}

// NewEngine builds an Engine. queue may be nil, in which case dispatch
// requests from agent responses are validated and logged but not enqueued.
//
// The engine reads agents, repos, skills, and backends from db on every
// event, there is no in-memory cfg cache. Static processor settings
// (concurrency cap, dispatch safety limits) are passed at construction
// because they never mutate via CRUD; CRUD touches the four entity sets
// only.
func NewEngine(st *store.Store, processorCfg config.ProcessorConfig, queue EventEnqueuer, logger zerolog.Logger) *Engine {
	max := processorCfg.MaxConcurrentAgents
	if max <= 0 {
		max = 4
	}
	eng := &Engine{
		store:         st,
		maxConcurrent: max,
		logger:        logger.With().Str("component", "workflow_engine").Logger(),
	}
	eng.runnerBuilder = eng.defaultRunnerFor
	if queue != nil {
		dedup := NewDispatchDedupStore(processorCfg.Dispatch.DedupWindowSeconds)
		eng.dispatcher = NewDispatcher(processorCfg.Dispatch, st, dedup, queue, logger)
	}
	return eng
}

// loadCfg reads the four entity sets from SQLite and returns a *config.Config
// scoped to a single event. The returned snapshot has only the fields the
// hot path looks at populated (agents, repos, skills, AIBackends); daemon-
// level fields the engine doesn't read are left zero.
func (e *Engine) loadCfg() (*config.Config, error) {
	agents, repos, skills, backends, err := e.store.ReadSnapshot()
	if err != nil {
		return nil, fmt.Errorf("engine: load cfg snapshot: %w", err)
	}
	cfg := &config.Config{
		Agents: agents,
		Repos:  repos,
		Skills: skills,
		Daemon: config.DaemonConfig{AIBackends: backends},
	}
	return cfg, nil
}

// defaultRunnerFor builds the ai.Runner that drives the AI CLI for the
// named backend. Built per-event so that backend changes via CRUD take
// effect immediately on the next event without any reload chain. The
// construction is cheap: a struct holding command + env + timeouts.
//
// Tests override the runner via WithRunnerBuilder so they can observe
// what the engine asked the runner to do without spawning a real CLI.
func (e *Engine) defaultRunnerFor(name string, b fleet.Backend) ai.Runner {
	var env map[string]string
	if b.LocalModelURL != "" {
		env = map[string]string{"ANTHROPIC_BASE_URL": b.LocalModelURL}
	}
	return ai.NewCommandRunner(
		name, "command", b.Command, env,
		b.TimeoutSeconds, b.MaxPromptChars,
		e.logger,
	)
}

// WithRunnerBuilder overrides the runner factory the engine uses to
// resolve a backend to an ai.Runner on each dispatch. Production wires
// the default that constructs an ai.NewCommandRunner; tests inject stub
// runners so they can observe the request the engine produced.
func (e *Engine) WithRunnerBuilder(fn func(name string, b fleet.Backend) ai.Runner) {
	e.runnerBuilder = fn
}

// WithTraceRecorder attaches an optional recorder that is called on each
// completed agent run. It is safe to call after NewEngine and before Run.
func (e *Engine) WithTraceRecorder(r TraceRecorder) {
	e.traceRec = r
}

// WithRunStreamPublisher attaches an optional collaborator the engine
// notifies on every run's lifecycle and stdout. Wires the AI CLI's
// per-line output through to observe.RunRegistry's per-span hub so the
// UI can stream the agent's "thinking" live.
func (e *Engine) WithRunStreamPublisher(p RunStreamPublisher) {
	e.streamPub = p
}

// WithRunTracker attaches an optional tracker that is called when an agent run
// starts and finishes. It is safe to call after NewEngine and before Run.
func (e *Engine) WithRunTracker(rt RunTracker) {
	e.runTracker = rt
}

// WithStepRecorder attaches an optional recorder that is called when an agent
// run produces a tool-loop transcript. It is safe to call after NewEngine and
// before Run.
func (e *Engine) WithStepRecorder(r StepRecorder) {
	e.stepRec = r
}

// WithLastRunRecorder attaches an optional recorder that is called after every
// cron-fired run completes. Production wires the autonomous
// scheduler here so its lastRuns map stays in sync with the actual runs
// flowing through the engine.
func (e *Engine) WithLastRunRecorder(r LastRunRecorder) {
	e.lastRunRec = r
}

// WithGraphRecorder attaches an optional recorder that is called on each
// inter-agent dispatch. It is safe to call after NewEngine and before Run.
func (e *Engine) WithGraphRecorder(r GraphRecorder) {
	e.graphRec = r
	if e.dispatcher != nil {
		e.dispatcher.WithGraphRecorder(r)
	}
}

// RunDispatchDedup blocks until ctx is cancelled, running the dispatch
// dedup eviction loop. Returns immediately when dispatch is not
// configured. The caller (typically the daemon's errgroup) owns
// goroutine creation and waits on Run for clean shutdown.
func (e *Engine) RunDispatchDedup(ctx context.Context) error {
	if e.dispatcher == nil {
		return nil
	}
	return e.dispatcher.dedup.Run(ctx)
}

// DispatchStats returns a snapshot of dispatch counters. Returns zero values
// (except RunsDeduped) when dispatch is not configured.
func (e *Engine) DispatchStats() DispatchStats {
	if e.dispatcher == nil {
		return DispatchStats{RunsDeduped: e.runsDeduped.Load()}
	}
	stats := e.dispatcher.Stats()
	stats.RunsDeduped = e.runsDeduped.Load()
	return stats
}

// Dispatcher returns the configured Dispatcher, or nil if dispatch is not
// enabled. The returned value can be shared with other components (e.g. the
// autonomous scheduler) so that all dispatch paths use the same safety limits
// and dedup store.
func (e *Engine) Dispatcher() *Dispatcher {
	return e.dispatcher
}

// HandleEvent runs every agent bound to ev.Repo whose binding matches ev.
// Label bindings (labels:) match when ev.Kind is a labeled event and the
// label in ev.Payload["label"] appears in the binding's label list.
// Event bindings (events:) match when ev.Kind appears in the binding's event
// list. The special kind "agent.dispatch" fires the target agent directly
// without binding lookup.
// Draft PR filtering and AI-label filtering happen at the webhook boundary
// before the event reaches the engine.
func (e *Engine) HandleEvent(ctx context.Context, ev Event) error {
	logBase := e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("kind", ev.Kind).
		Int("number", ev.Number).
		Str("actor", ev.Actor)
	if ev.ID != "" {
		logBase = logBase.Str("event_id", ev.ID)
	}
	logBase.Msg("processing event")

	if ev.Kind == "agent.dispatch" || ev.Kind == "agents.run" || ev.Kind == "cron" {
		return e.handleDispatchEvent(ctx, ev)
	}
	return e.fanOut(ctx, ev)
}

// handleDispatchEvent fires the target agent named in ev.Payload["target_agent"]
// directly, bypassing normal binding lookup.
func (e *Engine) handleDispatchEvent(ctx context.Context, ev Event) error {
	targetName, _ := ev.Payload["target_agent"].(string)
	if targetName == "" {
		return fmt.Errorf("agent.dispatch event missing target_agent in payload")
	}
	workspaceID := eventWorkspaceID(ev)

	// Read the four entity sets fresh from SQLite for this event. The
	// returned cfg is a per-event snapshot, no caching across events, no
	// reload chain. Cost is one SQLite snapshot read (~111µs for typical
	// fleet sizes); irrelevant at this daemon's traffic.
	cfg, err := e.loadCfg()
	if err != nil {
		return err
	}

	repo, ok := cfg.RepoByNameInWorkspace(ev.Repo.FullName, workspaceID)
	if !ok || !repo.Enabled {
		e.logger.Warn().Str("repo", ev.Repo.FullName).Msg("dispatch event for disabled or unknown repo, skipping")
		return nil
	}

	agent, ok := cfg.AgentByNameInWorkspace(targetName, workspaceID)
	if !ok {
		return fmt.Errorf("dispatch: target agent %q not found in workspace %q", targetName, workspaceID)
	}
	if !agentScopeAllowsRepo(agent, repo) {
		return fmt.Errorf("dispatch: target agent %q scope does not allow repo %q in workspace %q", targetName, repo.Name, workspaceID)
	}

	// agents.run events arrive from the HTTP /agents/run endpoint with no prior
	// dedup claim. Gate them here so two near-simultaneous on-demand requests for
	// the same (agent, repo) do not launch duplicate runs within the dedup window.
	//
	// agent.dispatch events skip this block: ProcessDispatches already claimed and
	// committed the dedup slot before enqueuing the event. Re-claiming would see
	// the committed entry and self-suppress every dispatched run. The enqueue-side
	// claim is the authoritative gate; handleDispatchEvent only executes it.
	if ev.Kind == "agents.run" && e.dispatcher != nil {
		dedupRepo := dedupRepoKey(workspaceID, repo.Name)
		if !e.dispatcher.dedup.TryClaimForDispatch(targetName, dedupRepo, ev.Number, time.Now()) {
			e.logger.Info().
				Str("repo", ev.Repo.FullName).
				Str("target", targetName).
				Msg("on-demand run skipped: agent already claimed within dedup window")
			e.runsDeduped.Add(1)
			return nil
		}
		e.dispatcher.dedup.MarkWebhookRunInFlight(targetName, dedupRepo, ev.Number)
	}

	// Autonomous (cron-fired) runs use the cron-namespace dedup window so a
	// cron tick that fires moments after a dispatch already claimed the slot
	// self-suppresses (matches the old scheduler.executeAgentRun behavior).
	// Rollback on error so the slot is freed for the next tick; finalize on
	// success so the dedup window is preserved.
	if ev.Kind == "cron" && e.dispatcher != nil {
		if !e.dispatcher.TryMarkAutonomousRun(workspaceID, targetName, repo.Name, time.Now()) {
			e.logger.Info().
				Str("repo", ev.Repo.FullName).
				Str("target", targetName).
				Msg("autonomous run skipped: dispatch already claimed within dedup window")
			e.runsDeduped.Add(1)
			return nil
		}
	}

	e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("target", targetName).
		Int("number", ev.Number).
		Str("invoked_by", ev.Actor).
		Str("kind", ev.Kind).
		Msg("running dispatched agent")

	runErr := e.runAgent(ctx, ev, agent, cfg)

	// Release the on-demand claim taken above for agents.run.
	if ev.Kind == "agents.run" && e.dispatcher != nil {
		dedupRepo := dedupRepoKey(workspaceID, repo.Name)
		if runErr != nil {
			e.dispatcher.dedup.AbandonWebhookRun(targetName, dedupRepo, ev.Number)
		} else {
			e.dispatcher.dedup.FinalizeWebhookRun(targetName, dedupRepo, ev.Number)
		}
	}
	// Release the cron-namespace mark taken above for autonomous runs.
	if ev.Kind == "cron" && e.dispatcher != nil {
		if runErr != nil {
			e.dispatcher.RollbackAutonomousRun(workspaceID, targetName, repo.Name)
		} else {
			e.dispatcher.FinalizeAutonomousRun(workspaceID, targetName, repo.Name)
		}
	}
	// Notify the autonomous scheduler so its lastRuns map (which drives the
	// per-binding schedule display in /agents) reflects this run's outcome.
	// Fired only for autonomous events, webhook/agents.run/dispatch runs
	// have their own provenance and don't update the cron schedule view.
	if ev.Kind == "cron" && e.lastRunRec != nil {
		status := "success"
		if runErr != nil {
			status = "error"
		}
		e.lastRunRec.RecordLastRun(workspaceID, targetName, repo.Name, time.Now(), status)
	}
	return runErr
}

// fanOut runs all agents matched for ev in parallel, capped by e.maxConcurrent.
// When a dedup store is configured, each agent run is gated through a
// TryClaim/CommitClaim/AbandonClaim sequence keyed on (agent, repo, number) so
// that concurrent or near-simultaneous events for the same item do not produce
// duplicate runs within the dedup window.
// A failing agent does not abort the others; all errors are joined and returned.
func (e *Engine) fanOut(ctx context.Context, ev Event) error {
	// Read the four entity sets from SQLite for this event. The cfg
	// snapshot scopes the agent lookup and the runAgent calls beneath it
	// to a single consistent epoch.
	cfg, err := e.loadCfg()
	if err != nil {
		return err
	}

	matched := e.agentsForEvent(cfg, ev)
	if len(matched) == 0 {
		e.logger.Info().
			Str("repo", ev.Repo.FullName).
			Str("kind", ev.Kind).
			Msg("no bindings matched event, skipping")
		return nil
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	sem := semaphore.NewWeighted(int64(e.maxConcurrent))
	for _, agent := range matched {
		if err := sem.Acquire(ctx, 1); err != nil {
			// Context cancelled before we could run all matched agents.
			break
		}
		wg.Add(1)
		go func(a fleet.Agent) {
			defer wg.Done()
			defer sem.Release(1)
			dedupRepo := dedupRepoKey(eventWorkspaceID(ev), ev.Repo.FullName)

			// Gate through the dedup store when configured, but only for
			// item-scoped events (number > 0).  Repo-level events such as
			// push have number=0 and must never be collapsed, each push is
			// a distinct event with a different head_sha, so two quick pushes
			// to the same repo should both trigger their bound agents.
			if e.dispatcher != nil && ev.Number > 0 {
				if !e.dispatcher.dedup.TryClaimForDispatch(a.Name, dedupRepo, ev.Number, time.Now()) {
					e.logger.Debug().
						Str("agent", a.Name).
						Str("repo", ev.Repo.FullName).
						Int("number", ev.Number).
						Msg("run skipped: agent already claimed within dedup window")
					e.runsDeduped.Add(1)
					return
				}
				// Increment the in-flight refcount so that the claim persists
				// past the TTL window for the duration of the run. Without this
				// a long-running agent (> dedup_window_seconds) would allow a
				// second identical event to pass the TTL check and start a
				// concurrent duplicate run.
				e.dispatcher.dedup.MarkWebhookRunInFlight(a.Name, dedupRepo, ev.Number)
			}

			// Abandon the in-flight marker and pending claim on panic so that
			// future events can retry. Only applies when a claim was taken (number > 0).
			defer func() {
				if r := recover(); r != nil {
					if e.dispatcher != nil && ev.Number > 0 {
						e.dispatcher.dedup.AbandonWebhookRun(a.Name, dedupRepo, ev.Number)
					}
					e.logger.Error().
						Interface("panic", r).
						Str("agent", a.Name).
						Str("repo", ev.Repo.FullName).
						Int("number", ev.Number).
						Msg("panic in agent run; claim abandoned")
					panic(r)
				}
			}()

			if err := e.runAgent(ctx, ev, a, cfg); err != nil {
				// Abandon on failure so that a retry or a subsequent event can
				// claim the slot and attempt the run again.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.AbandonWebhookRun(a.Name, dedupRepo, ev.Number)
				}
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			} else {
				// Commit the claim and release the in-flight marker. CommitClaim
				// marks the entry as committed so the TTL window stays active;
				// FinalizeWebhookRun decrements the refcount while preserving
				// the TTL entry, together they suppress duplicate runs until
				// the window expires without blocking new events after it does.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.CommitClaim(a.Name, dedupRepo, ev.Number)
					e.dispatcher.dedup.FinalizeWebhookRun(a.Name, dedupRepo, ev.Number)
				}
			}
		}(agent)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// agentsForEvent returns all enabled agents bound to ev.Repo whose binding
// matches ev. A label binding matches when ev is a labeled event and the
// payload label is in the binding's Labels slice. An event binding matches
// when ev.Kind appears in the binding's Events slice.
// cfg must be a snapshot already held by the caller to ensure a single
// consistent epoch across the lookup and the subsequent runAgent calls.
func (e *Engine) agentsForEvent(cfg *config.Config, ev Event) []fleet.Agent {
	workspaceID := eventWorkspaceID(ev)
	repo, ok := cfg.RepoByNameInWorkspace(ev.Repo.FullName, workspaceID)
	if !ok || !repo.Enabled {
		return nil
	}

	isLabeled := slices.Contains(labeledKinds, ev.Kind)
	label := ""
	if isLabeled {
		if v, ok := ev.Payload["label"]; ok {
			label, _ = v.(string)
		}
	}

	seen := make(map[string]struct{})
	var matched []fleet.Agent
	for _, b := range repo.Use {
		if !b.IsEnabled() {
			continue
		}
		var matches bool
		switch {
		case b.IsLabel() && isLabeled && label != "":
			matches = containsNormalized(b.Labels, label)
		case b.IsEvent():
			matches = containsNormalized(b.Events, ev.Kind)
		}
		if !matches {
			continue
		}
		if _, dup := seen[b.Agent]; dup {
			continue
		}
		agent, ok := cfg.AgentByNameInWorkspace(b.Agent, workspaceID)
		if !ok || !agentScopeAllowsRepo(agent, repo) {
			continue
		}
		seen[b.Agent] = struct{}{}
		matched = append(matched, agent)
	}
	return matched
}

// BuildRoster constructs the dispatch target roster for the current agent.
// Dispatch wiring is the authority: only agents in currentAgent.CanDispatch
// that exist, opt in, and have a description are visible.
func BuildRoster(cfg *config.Config, workspaceID, repoName, currentAgentName string) []ai.RosterEntry {
	repo, ok := cfg.RepoByNameInWorkspace(repoName, workspaceID)
	if !ok {
		return nil
	}
	currentAgent, ok := cfg.AgentByNameInWorkspace(currentAgentName, workspaceID)
	if !ok {
		return nil
	}

	var roster []ai.RosterEntry
	for _, targetName := range currentAgent.CanDispatch {
		target, ok := cfg.AgentByNameInWorkspace(targetName, workspaceID)
		if !ok || !target.AllowDispatch || target.Description == "" || !agentScopeAllowsRepo(target, repo) {
			continue
		}
		roster = append(roster, ai.RosterEntry{
			Name:          target.Name,
			Description:   target.Description,
			Skills:        target.Skills,
			AllowDispatch: true,
		})
	}
	return roster
}

// extractDispatchContext extracts root event ID and dispatch depth from ev.
// For non-dispatch events, it generates a new root event ID using ev.ID if
// set, or a fresh random ID.
func extractDispatchContext(ev Event) (rootEventID string, depth int) {
	if ev.Kind == "agent.dispatch" {
		rootEventID, _ = ev.Payload["root_event_id"].(string)
		if d, ok := ev.Payload["dispatch_depth"].(int); ok {
			depth = d
		}
		return rootEventID, depth
	}
	// Regular event: use event ID as root, depth 0.
	if ev.ID != "" {
		return ev.ID, 0
	}
	return GenEventID(), 0
}

// runAgent executes agent using the per-event cfg snapshot the caller
// loaded from SQLite. Backend resolution and runner construction happen
// here from that same snapshot, so the agent's backend, prompt, skills,
// and runner configuration all come from one consistent read.
func (e *Engine) runAgent(ctx context.Context, ev Event, agent fleet.Agent, cfg *config.Config) error {
	workspaceID := eventWorkspaceID(ev)
	backend := cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	backendCfg, ok := cfg.Daemon.AIBackends[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}
	if fleet.IsPinnedModelUnavailable(agent.Model, backendCfg) {
		return fmt.Errorf(
			"agent %q: configured model %q is not available for backend %q; run backend discovery and update the agent model",
			agent.Name,
			agent.Model,
			backend,
		)
	}

	rootEventID, dispatchDepth := extractDispatchContext(ev)

	// Build dispatch context fields for dispatched agents.
	var invokedBy, reason, parentSpanID string
	if ev.Kind == "agent.dispatch" {
		invokedBy, _ = ev.Payload["invoked_by"].(string)
		reason, _ = ev.Payload["reason"].(string)
		parentSpanID, _ = ev.Payload["parent_span_id"].(string)
	}

	// Serialise the read/run/write sequence for this (agent, repo) pair to
	// prevent a lost-update race on memory. Without this lock two overlapping
	// runs (cron tick + dispatch, two manual triggers, or any combination)
	// would both read the same old memory, run independently, and whichever
	// finishes last would silently clobber the other's persisted state.
	// Held across the entire read/run/write sequence even when memory is
	// disabled so concurrent runs of the same (agent, repo) still serialise
	// on dispatch and trace recording.
	runKey := workspaceID + "\x00" + agent.Name + "\x00" + ev.Repo.FullName
	e.runLock.acquire(runKey)
	defer e.runLock.release(runKey)

	// Build the roster of peer agents for this repo.
	roster := BuildRoster(cfg, workspaceID, ev.Repo.FullName, agent.Name)

	promptPayload := ev.Payload

	// Memory load is gated on the agent's AllowMemory flag and on a configured
	// backend. The same gate applies to the persist path below, so an agent
	// with AllowMemory=false neither reads nor writes memory regardless of the
	// trigger surface (event-driven, dispatched, or on-demand /run).
	memoryEnabled := agent.IsAllowMemory() && e.memory != nil
	var existingMemory string
	if memoryEnabled {
		// TODO(workspaces): scope memory by workspace once memory persistence
		// layout is rebuilt; runtime locking already includes workspace.
		mem, err := e.memory.ReadMemory(agent.Name, ev.Repo.FullName)
		if err != nil {
			return fmt.Errorf("agent %q: read memory: %w", agent.Name, err)
		}
		existingMemory = mem
	}

	guardrails, err := e.store.ReadEnabledGuardrails()
	if err != nil {
		return fmt.Errorf("agent %q: load guardrails: %w", agent.Name, err)
	}

	rendered, err := ai.RenderAgentPrompt(agent, cfg.Skills, guardrails, ai.PromptContext{
		Repo:          ev.Repo.FullName,
		Number:        ev.Number,
		Backend:       backend,
		EventKind:     ev.Kind,
		Actor:         ev.Actor,
		Payload:       promptPayload,
		Roster:        roster,
		InvokedBy:     invokedBy,
		Reason:        reason,
		RootEventID:   rootEventID,
		DispatchDepth: dispatchDepth,
		Memory:        existingMemory,
		HasMemory:     memoryEnabled,
	})
	if err != nil {
		return fmt.Errorf("agent %q: render prompt: %w", agent.Name, err)
	}
	workflow := fmt.Sprintf("%s:%s", backend, agent.Name)
	logger := e.logger.With().
		Str("repo", ev.Repo.FullName).
		Int("number", ev.Number).
		Str("agent", agent.Name).
		Str("backend", backend).
		Str("root_event_id", rootEventID).
		Int("dispatch_depth", dispatchDepth).
		Logger()
	if invokedBy != "" {
		logger = logger.With().Str("invoked_by", invokedBy).Logger()
	}

	spanStart := time.Now()
	spanID := GenEventID()
	composedPrompt := rendered.System
	if rendered.User != "" {
		if composedPrompt != "" {
			composedPrompt += "\n\n"
		}
		composedPrompt += rendered.User
	}

	if e.budgetStore != nil {
		// TODO(workspaces): include workspace in budget checks when budgets move
		// from global agent/backend limits to workspace-scoped policy.
		if err := e.budgetStore.CheckBudgetsWithLogger(backend, agent.Name, logger); err != nil {
			spanEnd := time.Now()
			var queueWaitMs int64
			if !ev.EnqueuedAt.IsZero() {
				queueWaitMs = spanStart.Sub(ev.EnqueuedAt).Milliseconds()
			}
			if e.traceRec != nil {
				status := "error"
				var exceeded *store.BudgetExceededError
				if errors.As(err, &exceeded) {
					status = "budget_exceeded"
				}
				e.traceRec.RecordSpan(SpanInput{
					SpanID:        spanID,
					RootEventID:   rootEventID,
					ParentSpanID:  parentSpanID,
					Agent:         agent.Name,
					Backend:       backend,
					Repo:          ev.Repo.FullName,
					EventKind:     ev.Kind,
					InvokedBy:     invokedBy,
					Number:        ev.Number,
					DispatchDepth: dispatchDepth,
					QueueWaitMs:   queueWaitMs,
					StartedAt:     spanStart,
					FinishedAt:    spanEnd,
					Status:        status,
					ErrorMsg:      err.Error(),
					Prompt:        composedPrompt,
				})
			}
			logger.Warn().Err(err).Msg("agent run rejected by token budget")
			return fmt.Errorf("agent %q: %w", agent.Name, err)
		}
	}

	logger.Info().Str("workflow", workflow).Msg("invoking ai agent")

	if e.runTracker != nil {
		e.runTracker.StartRun(agent.Name)
		defer e.runTracker.FinishRun(agent.Name)
	}

	runner := e.runnerBuilder(backend, backendCfg)

	// Live-stream registration: announce the run to the publisher so the
	// runners view can show an in-flight row with this span_id. The stream
	// body itself is backed by persisted trace_steps rows; RecordStep fans
	// each row out after it commits.
	var onLine func([]byte)
	if e.streamPub != nil {
		e.streamPub.BeginRun(BeginRunInput{
			SpanID:    spanID,
			EventID:   ev.ID,
			Agent:     agent.Name,
			Backend:   backend,
			Repo:      ev.Repo.FullName,
			EventKind: ev.Kind,
			StartedAt: spanStart,
		})
		defer e.streamPub.EndRun(spanID)
	}
	if e.stepRec != nil {
		parser := ai.NewTraceStepParser(backend)
		onLine = func(line []byte) {
			if parser == nil {
				return
			}
			for _, step := range parser(line) {
				e.stepRec.RecordStep(spanID, step)
			}
		}
	}
	resp, runErr := runner.Run(ctx, ai.Request{
		Workflow: workflow,
		Repo:     ev.Repo.FullName,
		Number:   ev.Number,
		Model:    agent.Model,
		System:   rendered.System,
		User:     rendered.User,
		OnLine:   onLine,
	})
	spanEnd := time.Now()

	// Compute queue-wait duration from when the event was enqueued to when
	// the runner started. Zero when EnqueuedAt is unset (e.g. cron events
	// created before this field existed).
	var queueWaitMs int64
	if !ev.EnqueuedAt.IsZero() {
		queueWaitMs = spanStart.Sub(ev.EnqueuedAt).Milliseconds()
	}

	// Record the trace span regardless of outcome. The composed prompt
	// (system + user) is captured so operators can inspect "what
	// exactly did the agent see" from the Traces / Runners UI; the
	// observe store gzips it before persistence. Token usage comes
	// from the runner's CLI parser.
	if e.traceRec != nil {
		// TODO(workspaces): persist workspace on trace spans when the observe
		// schema grows workspace-scoped event and trace filters.
		status, errMsg := "success", ""
		if runErr != nil {
			status = "error"
			errMsg = runErr.Error()
		}
		e.traceRec.RecordSpan(SpanInput{
			SpanID:           spanID,
			RootEventID:      rootEventID,
			ParentSpanID:     parentSpanID,
			Agent:            agent.Name,
			Backend:          backend,
			Repo:             ev.Repo.FullName,
			EventKind:        ev.Kind,
			InvokedBy:        invokedBy,
			Number:           ev.Number,
			DispatchDepth:    dispatchDepth,
			QueueWaitMs:      queueWaitMs,
			ArtifactsCount:   len(resp.Artifacts),
			Summary:          resp.Summary,
			StartedAt:        spanStart,
			FinishedAt:       spanEnd,
			Status:           status,
			ErrorMsg:         errMsg,
			Prompt:           composedPrompt,
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
		})
	}

	// Record transcript steps when available and the run was not already
	// parsed incrementally from stdout. The incremental path is used for
	// known streaming backends so live streams can replay and tail DB rows.
	if e.stepRec != nil && onLine == nil && len(resp.Steps) > 0 {
		e.stepRec.RecordSteps(spanID, resp.Steps)
	}

	if runErr != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, runErr)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("agent run completed")

	// Persist memory after a successful run, gated on the same flag that
	// controlled the load above. An empty resp.Memory is treated as "agent
	// did not return updated memory, preserve existing" rather than "wipe":
	// a structured-output omission must not silently destroy stored memory.
	// To explicitly clear, the agent must return a non-empty sentinel that
	// the operator opts into; the runtime's default is preserve-on-empty.
	// A write failure here is logged but does not fail the whole run: the
	// agent's GitHub-side artifacts are already in place,
	// and surfacing a memory-write error as a run failure would mask the
	// successful work that just landed. The next run reads from disk so the
	// stale state is naturally observable; if persistence is consistently
	// failing the operator will see it in logs.
	if memoryEnabled && resp.Memory != "" {
		// TODO(workspaces): write memory with the same workspace component used
		// by the load path once memory storage becomes workspace-scoped.
		if err := e.memory.WriteMemory(agent.Name, ev.Repo.FullName, resp.Memory); err != nil {
			logger.Error().Err(err).Msg("agent run completed but memory write failed")
		}
	}

	// Process any dispatch requests from the agent's response, threading the
	// current spanID so child runs can link back to their parent span.
	if e.dispatcher != nil && len(resp.Dispatch) > 0 {
		if err := e.dispatcher.ProcessDispatches(ctx, agent, ev, rootEventID, dispatchDepth, spanID, resp.Dispatch); err != nil {
			return fmt.Errorf("agent %q: dispatch: %w", agent.Name, err)
		}
	}

	return nil
}

func containsNormalized(haystack []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	return slices.ContainsFunc(haystack, func(s string) bool {
		return strings.ToLower(strings.TrimSpace(s)) == needle
	})
}

func eventWorkspaceID(ev Event) string {
	workspaceID := strings.TrimSpace(ev.WorkspaceID)
	if workspaceID == "" {
		return fleet.DefaultWorkspaceID
	}
	return workspaceID
}

func dedupRepoKey(workspaceID, repo string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		workspaceID = fleet.DefaultWorkspaceID
	}
	return workspaceID + "\x00" + repo
}

func agentScopeAllowsRepo(agent fleet.Agent, repo fleet.Repo) bool {
	agentWorkspace := strings.TrimSpace(agent.WorkspaceID)
	if agentWorkspace == "" {
		agentWorkspace = fleet.DefaultWorkspaceID
	}
	repoWorkspace := strings.TrimSpace(repo.WorkspaceID)
	if repoWorkspace == "" {
		repoWorkspace = fleet.DefaultWorkspaceID
	}
	if agentWorkspace != repoWorkspace {
		return false
	}

	scopeType := strings.TrimSpace(agent.ScopeType)
	if scopeType == "" {
		scopeType = "workspace"
	}
	switch scopeType {
	case "workspace":
		return strings.TrimSpace(agent.ScopeRepo) == ""
	case "repo":
		return fleet.NormalizeRepoName(agent.ScopeRepo) == repo.Name
	default:
		return false
	}
}
