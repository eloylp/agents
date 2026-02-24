package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

const (
	workflowIssueRefine = "issue_refine"
	workflowPRReview    = "pr_review"
)

type Engine struct {
	cfg     *config.Config
	runners map[string]ai.Runner
	prompts *ai.PromptStore
	logger  zerolog.Logger
}

func NewEngine(cfg *config.Config, runners map[string]ai.Runner, prompts *ai.PromptStore, logger zerolog.Logger) *Engine {
	return &Engine{
		cfg:     cfg,
		runners: runners,
		prompts: prompts,
		logger:  logger.With().Str("component", "workflow_engine").Logger(),
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
	backendCfg := e.cfg.AIBackends[resolvedBackend]
	var agents []string
	if agent == "all" {
		agents = backendCfg.Agents
	} else {
		if !slices.Contains(backendCfg.Agents, agent) {
			e.logger.Warn().Str("label", req.Label).Str("backend", resolvedBackend).Str("agent", agent).Int("pr_number", req.PR.Number).Str("repo", req.Repo.FullName).Msg("pr label references unsupported agent, skipping")
			return nil
		}
		agents = []string{agent}
	}
	runner := e.runners[resolvedBackend]
	logger := e.logger.With().
		Str("repo", req.Repo.FullName).
		Int("pr_number", req.PR.Number).
		Logger()
	// Errors are collected into agentErrs rather than returned via errgroup so
	// that a failing agent does not cancel the context and abort the others.
	// All errors are joined and returned once every goroutine has finished.
	var (
		mu        sync.Mutex
		agentErrs []error
	)
	group, groupCtx := errgroup.WithContext(ctx)
	for _, ag := range agents {
		group.Go(func() error {
			wf := fmt.Sprintf("%s:%s:%s", workflowPRReview, resolvedBackend, ag)
			prompt, err := e.prompts.PRReviewPrompt(ag, resolvedBackend, req.Repo.FullName, req.PR.Number)
			if err != nil {
				mu.Lock()
				agentErrs = append(agentErrs, fmt.Errorf("prompt %s/%s: %w", resolvedBackend, ag, err))
				mu.Unlock()
				return nil
			}
			logger.Info().Str("backend", resolvedBackend).Str("agent", ag).Msg("invoking ai agent for pr review")
			response, err := runner.Run(groupCtx, ai.Request{
				Workflow: wf,
				Repo:     req.Repo.FullName,
				Number:   req.PR.Number,
				Prompt:   prompt,
			})
			if err != nil {
				mu.Lock()
				agentErrs = append(agentErrs, fmt.Errorf("agent %s/%s: %w", resolvedBackend, ag, err))
				mu.Unlock()
				return nil
			}
			storedCount := len(response.Artifacts)
			logger.Info().Str("backend", resolvedBackend).Str("agent", ag).Int("artifacts_stored", storedCount).Msg("pr review completed")
			return nil
		})
	}
	_ = group.Wait()
	return errors.Join(agentErrs...)
}

// resolveBackend maps a label backend token to a configured backend name.
// An empty token falls back to the default configured backend. An explicit
// token that does not match any configured backend returns "" so the caller
// can skip the event rather than fail.
func (e *Engine) resolveBackend(backend string) string {
	if strings.TrimSpace(backend) == "" {
		defaultBackend := e.cfg.DefaultConfiguredBackend()
		if defaultBackend == "" {
			e.logger.Error().Msg("no default backend configured; expected one of claude or codex")
		}
		return defaultBackend
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	if _, ok := e.cfg.AIBackends[backend]; !ok {
		return ""
	}
	return backend
}
