package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"github.com/eloylp/agents/internal/ai"
)

// agentRegistry is the subset of *config.Config that Engine needs. Defining
// it here keeps the workflow package free of a compile-time dependency on the
// config package's concrete types.
type agentRegistry interface {
	AgentNames() []string
	HasAgent(name string) bool
	ResolveBackend(raw string) string
	MaxConcurrentAgents() int
}

const (
	workflowIssueRefine = "issue_refine"
	workflowPRReview    = "pr_review"

	// defaultMaxConcurrentAgents is the fallback used by NewEngine when the
	// caller provides a zero-value Config (e.g. in unit tests that construct
	// config.Config{} directly without running config.Load).  It matches the
	// default set by config.applyDefaults so behaviour is consistent.
	defaultMaxConcurrentAgents = 4
)

type Engine struct {
	cfg                 agentRegistry
	maxConcurrentAgents int
	runners             map[string]ai.Runner
	prompts             *ai.PromptStore
	logger              zerolog.Logger
}

func NewEngine(cfg agentRegistry, runners map[string]ai.Runner, prompts *ai.PromptStore, logger zerolog.Logger) *Engine {
	maxConcurrent := cfg.MaxConcurrentAgents()
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentAgents
	}
	return &Engine{
		cfg:                 cfg,
		maxConcurrentAgents: maxConcurrent,
		runners:             runners,
		prompts:             prompts,
		logger:              logger.With().Str("component", "workflow_engine").Logger(),
	}
}

func (e *Engine) HandleIssueLabelEvent(ctx context.Context, req IssueRequest) error {
	e.logger.Info().Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Str("label", req.Label).Msg("processing issue label event")
	backend, ok := ParseRefineLabel(req.Label)
	if !ok {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Str("label", req.Label).Msg("issue label skipped")
		return nil
	}
	selectedBackend := e.resolveBackend(backend)
	if selectedBackend == "" {
		e.logger.Warn().Str("label", req.Label).Int("issue_number", req.Issue.Number).Str("repo", req.Repo.FullName).Msg("issue label references unknown backend, skipping")
		return nil
	}
	logger := e.logger.With().
		Str("repo", req.Repo.FullName).
		Int("issue_number", req.Issue.Number).
		Logger()
	runner := e.runners[selectedBackend]
	prompt, err := e.prompts.IssueRefinePrompt(req.Repo.FullName, req.Issue.Number)
	if err != nil {
		return fmt.Errorf("issue refine prompt: %w", err)
	}
	logger.Info().Str("backend", selectedBackend).Msg("invoking ai backend for issue refinement")
	response, err := runner.Run(ctx, ai.Request{
		Workflow: fmt.Sprintf("%s:%s", workflowIssueRefine, selectedBackend),
		Repo:     req.Repo.FullName,
		Number:   req.Issue.Number,
		Prompt:   prompt,
	})
	if err != nil {
		return fmt.Errorf("agent %s: %w", selectedBackend, err)
	}
	storedCount := len(response.Artifacts)
	logger.Info().Str("backend", selectedBackend).Int("artifacts_stored", storedCount).Msg("issue refinement completed")
	return nil
}

func (e *Engine) HandlePullRequestLabelEvent(ctx context.Context, req PRRequest) error {
	e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Str("label", req.Label).Msg("processing pull request label event")
	if req.PR.Draft {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("pull request skipped, draft")
		return nil
	}
	backend, agent, ok := ParseReviewLabel(req.Label)
	if !ok {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Str("label", req.Label).Msg("pull request label skipped")
		return nil
	}
	resolvedBackend := e.resolveBackend(backend)
	if resolvedBackend == "" {
		e.logger.Warn().Str("label", req.Label).Int("pr_number", req.PR.Number).Str("repo", req.Repo.FullName).Msg("pr label references unknown backend, skipping")
		return nil
	}
	var agents []string
	if agent == "all" {
		agents = e.cfg.AgentNames()
	} else {
		if !e.cfg.HasAgent(agent) {
			e.logger.Warn().Str("label", req.Label).Str("backend", resolvedBackend).Str("agent", agent).Int("pr_number", req.PR.Number).Str("repo", req.Repo.FullName).Msg("pr label references unknown agent, skipping")
			return nil
		}
		agents = []string{agent}
	}
	runner := e.runners[resolvedBackend]
	logger := e.logger.With().
		Str("repo", req.Repo.FullName).
		Int("pr_number", req.PR.Number).
		Logger()
	// Errors are collected into agentErrs rather than propagated via context
	// cancellation so that a failing agent does not abort the remaining ones.
	// All errors are joined and returned once every goroutine has finished.
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		agentErrs []error
	)
	// sem limits the number of agent goroutines that run concurrently for this
	// event. The cap is per-event (not global) so a large fan-out on one PR
	// cannot starve another. Acquiring before spawning avoids goroutines that
	// immediately block on the semaphore.
	sem := semaphore.NewWeighted(int64(e.maxConcurrentAgents))
	for _, ag := range agents {
		if err := sem.Acquire(ctx, 1); err != nil {
			// ctx was cancelled; skip remaining agents.
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)
			wf := fmt.Sprintf("%s:%s:%s", workflowPRReview, resolvedBackend, ag)
			prompt, err := e.prompts.PRReviewPrompt(ag, resolvedBackend, req.Repo.FullName, req.PR.Number)
			if err != nil {
				mu.Lock()
				agentErrs = append(agentErrs, fmt.Errorf("prompt %s/%s: %w", resolvedBackend, ag, err))
				mu.Unlock()
				return
			}
			logger.Info().Str("backend", resolvedBackend).Str("agent", ag).Msg("invoking ai agent for pr review")
			response, err := runner.Run(ctx, ai.Request{
				Workflow: wf,
				Repo:     req.Repo.FullName,
				Number:   req.PR.Number,
				Prompt:   prompt,
			})
			if err != nil {
				mu.Lock()
				agentErrs = append(agentErrs, fmt.Errorf("agent %s/%s: %w", resolvedBackend, ag, err))
				mu.Unlock()
				return
			}
			storedCount := len(response.Artifacts)
			logger.Info().Str("backend", resolvedBackend).Str("agent", ag).Int("artifacts_stored", storedCount).Msg("pr review completed")
		}()
	}
	wg.Wait()
	return errors.Join(agentErrs...)
}

// resolveBackend maps a label backend token to a configured backend name.
// An empty token falls back to the default configured backend. An explicit
// token that does not match any configured backend returns "" so the caller
// can skip the event rather than fail.
func (e *Engine) resolveBackend(backend string) string {
	resolved := e.cfg.ResolveBackend(backend)
	if resolved == "" && strings.TrimSpace(backend) == "" {
		e.logger.Error().Msg("no default backend configured; expected one of claude or codex")
	}
	return resolved
}
