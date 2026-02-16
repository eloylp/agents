package workflow

import (
	"context"
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

func (e *Engine) HandleIssueLabelEvent(ctx context.Context, req IssueRequest) (bool, error) {
	if strings.TrimSpace(strings.ToLower(req.Action)) != "labeled" {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Str("action", req.Action).Str("label", req.Label).Msg("issue label event ignored")
		return false, nil
	}
	backend, ok := ParseRefineLabel(req.Label)
	if !ok {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("issue_number", req.Issue.Number).Str("label", req.Label).Msg("issue label skipped")
		return false, nil
	}
	selectedBackend := e.resolveBackend(backend)
	if selectedBackend == "" {
		e.logger.Warn().Str("label", req.Label).Int("issue_number", req.Issue.Number).Str("repo", req.Repo.FullName).Msg("issue label references unknown backend, skipping")
		return false, nil
	}
	logger := e.logger.With().
		Str("repo", req.Repo.FullName).
		Int("issue_number", req.Issue.Number).
		Logger()
	runner, ok := e.runners[selectedBackend]
	if !ok {
		logger.Warn().Str("backend", selectedBackend).Msg("runner missing for backend, skipping")
		return false, nil
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
		logger.Error().Err(err).Str("backend", selectedBackend).Msg("ai run failed")
		return false, nil
	}
	storedCount := len(response.Artifacts)
	logger.Info().Str("backend", selectedBackend).Int("artifacts_stored", storedCount).Msg("issue refinement completed")
	return true, nil
}

func (e *Engine) HandlePullRequestLabelEvent(ctx context.Context, req PRRequest) (bool, error) {
	if strings.TrimSpace(strings.ToLower(req.Action)) != "labeled" {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Str("action", req.Action).Str("label", req.Label).Msg("pull request label event ignored")
		return false, nil
	}
	if req.PR.Draft {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Msg("pull request skipped, draft")
		return false, nil
	}
	backend, agent, ok := ParseReviewLabel(req.Label)
	if !ok {
		e.logger.Info().Str("repo", req.Repo.FullName).Int("pr_number", req.PR.Number).Str("label", req.Label).Msg("pull request label skipped")
		return false, nil
	}
	resolvedBackend := e.resolveBackend(backend)
	if resolvedBackend == "" {
		e.logger.Warn().Str("label", req.Label).Int("pr_number", req.PR.Number).Str("repo", req.Repo.FullName).Msg("pr label references unknown backend, skipping")
		return false, nil
	}
	backendCfg := e.cfg.AIBackends[resolvedBackend]
	targets := map[string]map[string]struct{}{resolvedBackend: {}}
	if agent == "all" {
		for _, configuredAgent := range backendCfg.Agents {
			targets[resolvedBackend][configuredAgent] = struct{}{}
		}
	} else {
		if !slices.Contains(backendCfg.Agents, agent) {
			e.logger.Warn().Str("label", req.Label).Str("backend", resolvedBackend).Str("agent", agent).Int("pr_number", req.PR.Number).Str("repo", req.Repo.FullName).Msg("pr label references unsupported agent, skipping")
			return false, nil
		}
		targets[resolvedBackend][agent] = struct{}{}
	}
	return e.handlePRStateless(ctx, req.Repo, req.PR, targets)
}

func (e *Engine) handlePRStateless(ctx context.Context, repo config.RepoConfig, pr PullRequest, targets map[string]map[string]struct{}) (bool, error) {
	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("pr_number", pr.Number).
		Logger()

	type agentExecution struct {
		backend  string
		agent    string
		workflow string
		runner   ai.Runner
	}
	agentRuns := make([]agentExecution, 0)
	for backend, agents := range targets {
		runner, ok := e.runners[backend]
		if !ok {
			e.logger.Warn().Str("backend", backend).Msg("runner missing for backend, skipping")
			continue
		}
		for agent := range agents {
			agentRuns = append(agentRuns, agentExecution{
				backend:  backend,
				agent:    agent,
				workflow: fmt.Sprintf("%s:%s:%s", workflowPRReview, backend, agent),
				runner:   runner,
			})
		}
	}
	if len(agentRuns) == 0 {
		logger.Info().Msg("pull request skipped because no runnable agents were resolved")
		return false, nil
	}

	var (
		mu     sync.Mutex
		ranAny bool
	)
	group, groupCtx := errgroup.WithContext(ctx)
	for _, ar := range agentRuns {
		group.Go(func() error {
			prompt := ai.BuildPRReviewPrompt(ar.backend, ar.agent, repo.FullName, pr.Number)
			logger.Info().Str("backend", ar.backend).Str("agent", ar.agent).Msg("invoking ai agent for pr review")
			response, err := ar.runner.Run(groupCtx, ai.Request{
				Workflow: ar.workflow,
				Repo:     repo.FullName,
				Number:   pr.Number,
				Prompt:   prompt,
			})
			if err != nil {
				logger.Error().Err(err).Str("backend", ar.backend).Str("agent", ar.agent).Msg("ai run failed")
				return nil
			}
			storedCount := len(response.Artifacts)
			logger.Info().Str("backend", ar.backend).Str("agent", ar.agent).Int("artifacts_stored", storedCount).Msg("pr review completed")
			mu.Lock()
			ranAny = true
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return false, err
	}
	return ranAny, nil
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
