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
)

// labeledKinds are the event kinds that trigger label-based bindings.
var labeledKinds = []string{"issues.labeled", "pull_request.labeled"}

// TraceRecorder is an optional observer that the Engine calls when an agent
// run completes. Implementations must be safe for concurrent use.
type TraceRecorder interface {
	RecordSpan(spanID, rootEventID, parentSpanID, agent, backend, repo, eventKind, invokedBy string, number, dispatchDepth int, queueWaitMs int64, artifactsCount int, summary string, startedAt, finishedAt time.Time, status, errMsg string)
}

// RunTracker is an optional observer that the Engine calls when an agent run
// starts and finishes. It is used to report which agents are currently active.
// Implementations must be safe for concurrent use.
type RunTracker interface {
	StartRun(agentName string)
	FinishRun(agentName string)
}

// GraphRecorder is an optional observer that the Engine calls when a dispatch
// is issued. Implementations must be safe for concurrent use.
type GraphRecorder interface {
	RecordDispatch(from, to, repo string, number int, reason string)
}

// Engine dispatches workflow events to the agents bound to the target repo.
// It routes each event by matching against label bindings (labels:) for labeled
// events and against event bindings (events:) for all event kinds.
// The special kind "agent.dispatch" bypasses binding lookup and fires the
// target agent named in the payload directly.
// Agent resolution, backend selection, and prompt composition all happen here;
// the runners just execute the resulting prompt.
type Engine struct {
	cfgPtr        atomic.Pointer[config.Config]
	runners       map[string]ai.Runner
	dispatcher    *Dispatcher
	maxConcurrent int
	logger        zerolog.Logger
	traceRec      TraceRecorder
	graphRec      GraphRecorder
	runTracker    RunTracker
	runsDeduped   atomic.Int64
}

// cfg returns the current effective configuration. All engine methods must
// call this instead of reading the field directly so live reloads are visible.
func (e *Engine) cfg() *config.Config {
	return e.cfgPtr.Load()
}

// UpdateConfig atomically replaces the engine's config and updates the
// dispatcher's agent map so that the next event uses new routing definitions.
// It is safe to call concurrently with HandleEvent.
func (e *Engine) UpdateConfig(cfg *config.Config) {
	e.cfgPtr.Store(cfg)
	if e.dispatcher != nil {
		agentMap := make(map[string]config.AgentDef, len(cfg.Agents))
		for _, a := range cfg.Agents {
			agentMap[a.Name] = a
		}
		e.dispatcher.UpdateAgents(agentMap)
	}
}

// NewEngine builds an Engine. queue may be nil, in which case dispatch
// requests from agent responses are validated and logged but not enqueued.
func NewEngine(cfg *config.Config, runners map[string]ai.Runner, queue EventEnqueuer, logger zerolog.Logger) *Engine {
	max := cfg.Daemon.Processor.MaxConcurrentAgents
	if max <= 0 {
		max = 4
	}
	eng := &Engine{
		runners:       runners,
		maxConcurrent: max,
		logger:        logger.With().Str("component", "workflow_engine").Logger(),
	}
	eng.cfgPtr.Store(cfg)
	if queue != nil {
		agentMap := make(map[string]config.AgentDef, len(cfg.Agents))
		for _, a := range cfg.Agents {
			agentMap[a.Name] = a
		}
		dedup := NewDispatchDedupStore(cfg.Daemon.Processor.Dispatch.DedupWindowSeconds)
		eng.dispatcher = NewDispatcher(cfg.Daemon.Processor.Dispatch, agentMap, dedup, queue, logger)
	}
	return eng
}

// WithTraceRecorder attaches an optional recorder that is called on each
// completed agent run. It is safe to call after NewEngine and before Run.
func (e *Engine) WithTraceRecorder(r TraceRecorder) {
	e.traceRec = r
}

// WithRunTracker attaches an optional tracker that is called when an agent run
// starts and finishes. It is safe to call after NewEngine and before Run.
func (e *Engine) WithRunTracker(rt RunTracker) {
	e.runTracker = rt
}

// WithGraphRecorder attaches an optional recorder that is called on each
// inter-agent dispatch. It is safe to call after NewEngine and before Run.
func (e *Engine) WithGraphRecorder(r GraphRecorder) {
	e.graphRec = r
	if e.dispatcher != nil {
		e.dispatcher.WithGraphRecorder(r)
	}
}

// StartDispatchDedup starts the background eviction loop for the dispatch
// dedup store. It is a no-op when dispatch is not configured.
func (e *Engine) StartDispatchDedup(ctx context.Context) {
	if e.dispatcher != nil {
		e.dispatcher.dedup.Start(ctx)
	}
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

	if ev.Kind == "agent.dispatch" || ev.Kind == "agents.run" {
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

	repo, ok := e.cfg().RepoByName(ev.Repo.FullName)
	if !ok || !repo.Enabled {
		e.logger.Warn().Str("repo", ev.Repo.FullName).Msg("dispatch event for disabled or unknown repo, skipping")
		return nil
	}

	// Target must be bound to this repo (any trigger kind is sufficient).
	if !slices.ContainsFunc(repo.Use, func(b config.Binding) bool {
		return b.Agent == targetName && b.IsEnabled()
	}) {
		return fmt.Errorf("dispatch: target agent %q is not bound to repo %q", targetName, ev.Repo.FullName)
	}

	agent, ok := e.cfg().AgentByName(targetName)
	if !ok {
		return fmt.Errorf("dispatch: target agent %q not found", targetName)
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
		if !e.dispatcher.dedup.TryClaimForDispatch(targetName, repo.Name, ev.Number, time.Now()) {
			e.logger.Info().
				Str("repo", ev.Repo.FullName).
				Str("target", targetName).
				Msg("on-demand run skipped: agent already claimed within dedup window")
			e.runsDeduped.Add(1)
			return nil
		}
		e.dispatcher.dedup.MarkWebhookRunInFlight(targetName, repo.Name, ev.Number)
	}

	e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("target", targetName).
		Int("number", ev.Number).
		Str("invoked_by", ev.Actor).
		Msg("running dispatched agent")

	runErr := e.runAgent(ctx, ev, agent)

	// Release the on-demand claim taken above for agents.run.
	if ev.Kind == "agents.run" && e.dispatcher != nil {
		if runErr != nil {
			e.dispatcher.dedup.AbandonWebhookRun(targetName, repo.Name, ev.Number)
		} else {
			e.dispatcher.dedup.FinalizeWebhookRun(targetName, repo.Name, ev.Number)
		}
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
	matched := e.agentsForEvent(ev)
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
		go func(a config.AgentDef) {
			defer wg.Done()
			defer sem.Release(1)

			// Gate through the dedup store when configured, but only for
			// item-scoped events (number > 0).  Repo-level events such as
			// push have number=0 and must never be collapsed — each push is
			// a distinct event with a different head_sha, so two quick pushes
			// to the same repo should both trigger their bound agents.
			if e.dispatcher != nil && ev.Number > 0 {
				if !e.dispatcher.dedup.TryClaimForDispatch(a.Name, ev.Repo.FullName, ev.Number, time.Now()) {
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
				e.dispatcher.dedup.MarkWebhookRunInFlight(a.Name, ev.Repo.FullName, ev.Number)
			}

			// Abandon the in-flight marker and pending claim on panic so that
			// future events can retry. Only applies when a claim was taken (number > 0).
			defer func() {
				if r := recover(); r != nil {
					if e.dispatcher != nil && ev.Number > 0 {
						e.dispatcher.dedup.AbandonWebhookRun(a.Name, ev.Repo.FullName, ev.Number)
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

			if err := e.runAgent(ctx, ev, a); err != nil {
				// Abandon on failure so that a retry or a subsequent event can
				// claim the slot and attempt the run again.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.AbandonWebhookRun(a.Name, ev.Repo.FullName, ev.Number)
				}
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			} else {
				// Commit the claim and release the in-flight marker. CommitClaim
				// marks the entry as committed so the TTL window stays active;
				// FinalizeWebhookRun decrements the refcount while preserving
				// the TTL entry — together they suppress duplicate runs until
				// the window expires without blocking new events after it does.
				if e.dispatcher != nil && ev.Number > 0 {
					e.dispatcher.dedup.CommitClaim(a.Name, ev.Repo.FullName, ev.Number)
					e.dispatcher.dedup.FinalizeWebhookRun(a.Name, ev.Repo.FullName, ev.Number)
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
func (e *Engine) agentsForEvent(ev Event) []config.AgentDef {
	repo, ok := e.cfg().RepoByName(ev.Repo.FullName)
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
	var matched []config.AgentDef
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
		agent, ok := e.cfg().AgentByName(b.Agent)
		if !ok {
			continue
		}
		seen[b.Agent] = struct{}{}
		matched = append(matched, agent)
	}
	return matched
}

// BuildRoster constructs the roster of peer agents for the given repo and
// agent name. The current agent is excluded. It is shared with the autonomous
// scheduler to avoid duplicating the dedup+lookup logic.
func BuildRoster(cfg *config.Config, repoName, currentAgentName string) []ai.RosterEntry {
	repo, ok := cfg.RepoByName(repoName)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	var roster []ai.RosterEntry
	for _, b := range repo.Use {
		if !b.IsEnabled() || b.Agent == currentAgentName {
			continue
		}
		if _, dup := seen[b.Agent]; dup {
			continue
		}
		agent, ok := cfg.AgentByName(b.Agent)
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

func (e *Engine) runAgent(ctx context.Context, ev Event, agent config.AgentDef) error {
	backend := e.cfg().ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	runner, ok := e.runners[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}

	rootEventID, dispatchDepth := extractDispatchContext(ev)

	// Build dispatch context fields for dispatched agents.
	var invokedBy, reason, parentSpanID string
	if ev.Kind == "agent.dispatch" {
		invokedBy, _ = ev.Payload["invoked_by"].(string)
		reason, _ = ev.Payload["reason"].(string)
		parentSpanID, _ = ev.Payload["parent_span_id"].(string)
	}

	// Build the roster of peer agents for this repo.
	roster := BuildRoster(e.cfg(), ev.Repo.FullName, agent.Name)

	promptPayload := ev.Payload

	rendered, err := ai.RenderAgentPrompt(agent, e.cfg().Skills, ai.PromptContext{
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
	if !agent.AllowPRs {
		rendered.System = "Do not open or create pull requests under any circumstances.\n" + rendered.System
	}
	logger.Info().Str("workflow", workflow).Msg("invoking ai agent")

	if e.runTracker != nil {
		e.runTracker.StartRun(agent.Name)
		defer e.runTracker.FinishRun(agent.Name)
	}

	spanStart := time.Now()
	spanID := GenEventID()
	resp, runErr := runner.Run(ctx, ai.Request{
		Workflow: workflow,
		Repo:     ev.Repo.FullName,
		Number:   ev.Number,
		System:   rendered.System,
		User:     rendered.User,
	})
	spanEnd := time.Now()

	// Compute queue-wait duration from when the event was enqueued to when
	// the runner started. Zero when EnqueuedAt is unset (e.g. cron events
	// created before this field existed).
	var queueWaitMs int64
	if !ev.EnqueuedAt.IsZero() {
		queueWaitMs = spanStart.Sub(ev.EnqueuedAt).Milliseconds()
	}

	// Record the trace span regardless of outcome.
	if e.traceRec != nil {
		status, errMsg := "success", ""
		if runErr != nil {
			status = "error"
			errMsg = runErr.Error()
		}
		e.traceRec.RecordSpan(
			spanID, rootEventID, parentSpanID,
			agent.Name, backend,
			ev.Repo.FullName, ev.Kind, invokedBy,
			ev.Number, dispatchDepth,
			queueWaitMs, len(resp.Artifacts), resp.Summary,
			spanStart, spanEnd,
			status, errMsg,
		)
	}

	if runErr != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, runErr)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("agent run completed")

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
