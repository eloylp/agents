package workflow

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	runtimeexec "github.com/eloylp/agents/internal/runtime"
	"github.com/eloylp/agents/internal/store"
)

// labeledKinds are the event kinds that trigger label-based bindings.
var labeledKinds = []string{"issues.labeled", "pull_request.labeled"}

// directEventKinds bypass binding fan-out and target a specific agent.
var directEventKinds = []string{"agent.dispatch", "agents.run", "cron"}

// SpanInput is the call shape for TraceRecorder.RecordSpan. Defined
// here as well as in the observe package so the engine doesn't import
// observe just to name the struct; the two are kept structurally
// identical and the observe.Store.RecordSpan adapter accepts this
// shape directly via a thin wrapper at construction time. Keeping the
// type local mirrors how the engine treats every other recorder.
type SpanInput struct {
	SpanID, RootEventID, ParentSpanID string
	WorkspaceID                       string
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
	SpanID, EventID, WorkspaceID, Agent, Backend, Repo, EventKind string
	StartedAt                                                     time.Time
}

// GraphRecorder is an optional observer that the Engine calls when a dispatch
// is issued. Implementations must be safe for concurrent use.
type GraphRecorder interface {
	RecordDispatch(workspaceID, from, to, repo string, number int, reason string)
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
	ReadMemory(workspace, agent, repo string) (string, error)
	WriteMemory(workspace, agent, repo, content string) error
}

type Engine struct {
	store         *store.Store
	runnerBuilder func(workspaceID string, name string, b fleet.Backend) ai.Runner
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
	dockerMu      sync.Mutex
	dockerRunner  runtimeexec.Runner
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

// WithRunnerBuilder overrides the runner factory the engine uses to
// resolve a backend to an ai.Runner on each dispatch. Production wires
// the default that constructs a container-backed AI runner; tests inject stub
// runners so they can observe the request the engine produced.
func (e *Engine) WithRunnerBuilder(fn func(workspaceID string, name string, b fleet.Backend) ai.Runner) {
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

	if slices.Contains(directEventKinds, ev.Kind) {
		return e.handleDispatchEvent(ctx, ev)
	}
	return e.fanOut(ctx, ev)
}
