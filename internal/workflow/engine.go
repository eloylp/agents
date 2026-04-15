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
// Agent resolution, backend selection, and prompt composition all happen here;
// the runners just execute the resulting prompt.
type Engine struct {
	cfg           *config.Config
	runners       map[string]ai.Runner
	maxConcurrent int
	logger        zerolog.Logger
}

func NewEngine(cfg *config.Config, runners map[string]ai.Runner, logger zerolog.Logger) *Engine {
	max := cfg.Daemon.Processor.MaxConcurrentAgents
	if max <= 0 {
		max = 4
	}
	return &Engine{
		cfg:           cfg,
		runners:       runners,
		maxConcurrent: max,
		logger:        logger.With().Str("component", "workflow_engine").Logger(),
	}
}

// HandleEvent runs every agent bound to ev.Repo whose binding matches ev.
// Label bindings (labels:) match when ev.Kind is a labeled event and the
// label in ev.Payload["label"] appears in the binding's label list.
// Event bindings (events:) match when ev.Kind appears in the binding's event
// list. Draft PR filtering and AI-label filtering happen at the webhook
// boundary before the event reaches the engine.
func (e *Engine) HandleEvent(ctx context.Context, ev Event) error {
	e.logger.Info().
		Str("repo", ev.Repo.FullName).
		Str("kind", ev.Kind).
		Int("number", ev.Number).
		Str("actor", ev.Actor).
		Msg("processing event")
	return e.dispatch(ctx, ev)
}

// dispatch runs all agents matched for ev in parallel, capped by e.maxConcurrent.
// A failing agent does not abort the others; all errors are joined and returned.
func (e *Engine) dispatch(ctx context.Context, ev Event) error {
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

func (e *Engine) runAgent(ctx context.Context, ev Event, agent config.AgentDef) error {
	backend := e.cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	runner, ok := e.runners[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}
	prompt, err := ai.RenderAgentPrompt(agent, e.cfg.Skills, ai.PromptContext{
		Repo:      ev.Repo.FullName,
		Number:    ev.Number,
		Backend:   backend,
		EventKind: ev.Kind,
		Actor:     ev.Actor,
		Payload:   ev.Payload,
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
		Logger()
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
	return nil
}

func containsNormalized(haystack []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	return slices.ContainsFunc(haystack, func(s string) bool {
		return strings.ToLower(strings.TrimSpace(s)) == needle
	})
}
