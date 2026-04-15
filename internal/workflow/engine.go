package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

// labeledKinds are the event kinds that trigger label-based bindings.
var labeledKinds = []string{"issues.labeled", "pull_request.labeled"}

// Engine dispatches workflow events to the agents bound to the target repo.
// It routes each event by matching against label bindings (labels:) for labeled
// events and against event bindings (events:) for all event kinds.
// The special kind "agent.dispatch" bypasses binding lookup and fires the
// target agent named in the payload directly.
// Agent resolution, backend selection, and prompt composition all happen here;
// the runners just execute the resulting prompt.
type Engine struct {
	cfg           *config.Config
	runners       map[string]ai.Runner
	dispatcher    *Dispatcher
	maxConcurrent int
	logger        zerolog.Logger
}

// NewEngine builds an Engine. queue may be nil, in which case dispatch
// requests from agent responses are validated and logged but not enqueued.
func NewEngine(cfg *config.Config, runners map[string]ai.Runner, queue EventEnqueuer, logger zerolog.Logger) *Engine {
	max := cfg.Daemon.Processor.MaxConcurrentAgents
	if max <= 0 {
		max = 4
	}
	eng := &Engine{
		cfg:           cfg,
		runners:       runners,
		maxConcurrent: max,
		logger:        logger.With().Str("component", "workflow_engine").Logger(),
	}
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

// StartDispatchDedup starts the background eviction loop for the dispatch
// dedup store. It is a no-op when dispatch is not configured.
func (e *Engine) StartDispatchDedup(ctx context.Context) {
	if e.dispatcher != nil {
		e.dispatcher.dedup.Start(ctx)
	}
}

// DispatchStats returns a snapshot of dispatch counters. Returns zero values
// when dispatch is not configured.
func (e *Engine) DispatchStats() DispatchStats {
	if e.dispatcher == nil {
		return DispatchStats{}
	}
	return e.dispatcher.Stats()
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

	if ev.Kind == "agent.dispatch" {
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

	repo, ok := e.cfg.RepoByName(ev.Repo.FullName)
	if !ok || !repo.Enabled {
		e.logger.Warn().Str("repo", ev.Repo.FullName).Msg("dispatch event for disabled or unknown repo, skipping")
		return nil
	}

	// Target must be bound to this repo (any trigger kind is sufficient).
	bound := false
	for _, b := range repo.Use {
		if b.Agent == targetName && b.IsEnabled() {
			bound = true
			break
		}
	}
	if !bound {
		return fmt.Errorf("dispatch: target agent %q is not bound to repo %q", targetName, ev.Repo.FullName)
	}

	agent, ok := e.cfg.AgentByName(targetName)
	if !ok {
		return fmt.Errorf("dispatch: target agent %q not found", targetName)
	}

	e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("target", targetName).
		Int("number", ev.Number).
		Str("invoked_by", ev.Actor).
		Msg("running dispatched agent")

	return e.runAgent(ctx, ev, agent)
}

// fanOut runs all agents matched for ev in parallel, capped by e.maxConcurrent.
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
			if err := e.runAgent(ctx, ev, a); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
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
	repo, ok := e.cfg.RepoByName(ev.Repo.FullName)
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
		agent, ok := e.cfg.AgentByName(b.Agent)
		if !ok {
			continue
		}
		seen[b.Agent] = struct{}{}
		matched = append(matched, agent)
	}
	return matched
}

// buildRoster constructs the roster of peer agents for the given repo and
// agent name. The current agent is excluded.
func (e *Engine) buildRoster(repoName, currentAgentName string) []ai.RosterEntry {
	repo, ok := e.cfg.RepoByName(repoName)
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
		agent, ok := e.cfg.AgentByName(b.Agent)
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
	backend := e.cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	runner, ok := e.runners[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}

	rootEventID, dispatchDepth := extractDispatchContext(ev)

	// Build dispatch context fields for dispatched agents.
	var invokedBy, reason string
	if ev.Kind == "agent.dispatch" {
		invokedBy, _ = ev.Payload["invoked_by"].(string)
		reason, _ = ev.Payload["reason"].(string)
	}

	// Build the roster of peer agents for this repo.
	roster := e.buildRoster(ev.Repo.FullName, agent.Name)

	promptPayload := ev.Payload

	prompt, err := ai.RenderAgentPrompt(agent, e.cfg.Skills, ai.PromptContext{
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
	logger.Info().Str("workflow", workflow).Msg("invoking ai agent")
	resp, err := runner.Run(ctx, ai.Request{
		Workflow: workflow,
		Repo:     ev.Repo.FullName,
		Number:   ev.Number,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("agent run completed")

	// Process any dispatch requests from the agent's response.
	if e.dispatcher != nil && len(resp.Dispatch) > 0 {
		e.dispatcher.ProcessDispatches(ctx, agent, ev, rootEventID, dispatchDepth, resp.Dispatch)
	}

	return nil
}

func containsNormalized(haystack []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	return slices.ContainsFunc(haystack, func(s string) bool {
		return strings.ToLower(strings.TrimSpace(s)) == needle
	})
}
