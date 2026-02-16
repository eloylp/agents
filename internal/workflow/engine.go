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
	logger  zerolog.Logger
}

func NewEngine(cfg *config.Config, runners map[string]ai.Runner, logger zerolog.Logger) *Engine {
	return &Engine{
		cfg:     cfg,
		runners: runners,
		logger:  logger.With().Str("component", "workflow_engine").Logger(),
	}
}

func (e *Engine) HandleIssueLabelEvent(ctx context.Context, req IssueRequest) error {
	if strings.TrimSpace(strings.ToLower(req.Action)) != "labeled" {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Str("action", req.Action).Str("label", req.Label).Msg("issue label event ignored")
		return nil
	}
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
	runner, ok := e.runners[selectedBackend]
	if !ok {
		logger.Warn().Str("backend", selectedBackend).Msg("runner missing for backend, skipping")
		return nil
	}
	prompt := ai.BuildIssueRefinePrompt(req.Repo.FullName, req.Issue.Number)
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
	if strings.TrimSpace(strings.ToLower(req.Action)) != "labeled" {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Str("action", req.Action).Str("label", req.Label).Msg("pull request label event ignored")
		return nil
	}
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
	runner, ok := e.runners[resolvedBackend]
	if !ok {
		e.logger.Warn().Str("backend", resolvedBackend).Msg("runner missing for backend, skipping")
		return nil
	}
	logger := e.logger.With().
		Str("repo", req.Repo.FullName).
		Int("pr_number", req.PR.Number).
		Logger()
	var (
		mu      sync.Mutex
		agentErrs []error
	)
	group, groupCtx := errgroup.WithContext(ctx)
	for _, ag := range agents {
		group.Go(func() error {
			wf := fmt.Sprintf("%s:%s:%s", workflowPRReview, resolvedBackend, ag)
			prompt := ai.BuildPRReviewPrompt(resolvedBackend, ag, req.Repo.FullName, req.PR.Number)
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

func (e *Engine) resolveBackend(backend string) string {
	if strings.TrimSpace(backend) == "" {
		defaultAgent := e.cfg.DefaultConfiguredBackend()
		if defaultAgent == "" {
			e.logger.Error().Msg("no default agent configured; expected one of claude or codex")
		}
		return defaultAgent
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	if _, ok := e.cfg.AIBackends[backend]; !ok {
		return ""
	}
	return backend
}
