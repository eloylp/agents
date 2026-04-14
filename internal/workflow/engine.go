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

// Engine dispatches label-triggered workflow events to the agents bound to
// the target repo. Agent resolution, backend selection, and prompt
// composition all happen here; the runners just execute the resulting prompt.
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

// HandleLabelEvent runs every agent bound to ev.Repo whose binding includes
// ev.Label. Draft PRs are skipped.
func (e *Engine) HandleLabelEvent(ctx context.Context, ev LabelEvent) error {
	e.logger.Info().Str("repo", ev.Repo.FullName).Str("kind", ev.Kind).Int("number", ev.Number).Str("label", ev.Label).Msg("processing label event")
	if ev.Kind == "pr" && ev.Draft {
		e.logger.Info().Str("repo", ev.Repo.FullName).Int("number", ev.Number).Msg("pull request skipped, draft")
		return nil
	}
	return e.dispatch(ctx, ev.Repo.FullName, ev.Label, ev.Number, ev.Kind)
}

// dispatch runs all agents bound to the given repo with a label matching
// `label`. Runs are parallel, capped by e.maxConcurrent. A failing agent
// does not abort the others; all errors are joined and returned.
func (e *Engine) dispatch(ctx context.Context, repoName, label string, number int, kind string) error {
	matched := e.agentsForLabel(repoName, label)
	if len(matched) == 0 {
		e.logger.Info().Str("repo", repoName).Str("label", label).Msg("no bindings matched label, skipping")
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
			if err := e.runAgent(ctx, repoName, number, a, kind); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(agent)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// agentsForLabel returns all enabled agents bound to repoName with a binding
// whose Labels slice includes label.
func (e *Engine) agentsForLabel(repoName, label string) []config.AgentDef {
	repo, ok := e.cfg.RepoByName(repoName)
	if !ok || !repo.Enabled {
		return nil
	}
	seen := make(map[string]struct{})
	var matched []config.AgentDef
	for _, b := range repo.Use {
		if !b.IsEnabled() || !b.IsLabel() {
			continue
		}
		if !containsLabel(b.Labels, label) {
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

func (e *Engine) runAgent(ctx context.Context, repo string, number int, agent config.AgentDef, kind string) error {
	backend := e.cfg.ResolveBackend(agent.Backend)
	if backend == "" {
		return fmt.Errorf("agent %q: no runner available for backend %q", agent.Name, agent.Backend)
	}
	runner, ok := e.runners[backend]
	if !ok {
		return fmt.Errorf("agent %q: no runner for backend %q", agent.Name, backend)
	}
	prompt, err := ai.RenderAgentPrompt(agent, e.cfg.Skills, ai.PromptContext{
		Repo:    repo,
		Number:  number,
		Backend: backend,
	})
	if err != nil {
		return fmt.Errorf("agent %q: render prompt: %w", agent.Name, err)
	}
	workflow := fmt.Sprintf("%s:%s:%s", kind, backend, agent.Name)
	logger := e.logger.With().Str("repo", repo).Int("number", number).Str("agent", agent.Name).Str("backend", backend).Logger()
	logger.Info().Str("workflow", workflow).Msg("invoking ai agent")
	resp, err := runner.Run(ctx, ai.Request{
		Workflow: workflow,
		Repo:     repo,
		Number:   number,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	logger.Info().Int("artifacts_stored", len(resp.Artifacts)).Msg("agent run completed")
	return nil
}

func containsLabel(labels []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	return slices.ContainsFunc(labels, func(l string) bool {
		return strings.ToLower(strings.TrimSpace(l)) == target
	})
}
