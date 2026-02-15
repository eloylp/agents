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

func (e *Engine) HandleIssueLabelEvent(ctx context.Context, repo config.RepoConfig, issue Issue, action, labelName string) (bool, error) {
	if strings.TrimSpace(strings.ToLower(action)) != "labeled" {
		e.logger.Info().Str("repo", repo.FullName).Int("issue_number", issue.Number).Str("action", action).Str("label", labelName).Msg("issue label event ignored")
		return false, nil
	}
	workflow, agent, _, ok := ParseAILabel(labelName)
	if !ok || workflow != workflowIssueRefine {
		e.logger.Info().Str("repo", repo.FullName).Int("issue_number", issue.Number).Str("label", labelName).Msg("issue label skipped")
		return false, nil
	}
	selectedBackend := e.resolveBackend(agent)
	if selectedBackend == "" {
		e.logger.Warn().Str("label", labelName).Int("issue_number", issue.Number).Str("repo", repo.FullName).Msg("issue label references unknown agent, skipping")
		return false, nil
	}
	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("issue_number", issue.Number).
		Logger()
	runner, ok := e.runners[selectedBackend]
	if !ok {
		logger.Warn().Str("backend", selectedBackend).Msg("runner missing for backend, skipping")
		return false, nil
	}
	prompt := ai.BuildIssueRefinePrompt(selectedBackend, repo.FullName, issue.Number)
	logger.Info().Str("backend", selectedBackend).Msg("invoking ai backend for issue refinement")
	response, err := runner.Run(ctx, ai.Request{
		Workflow: fmt.Sprintf("%s:%s", workflowIssueRefine, selectedBackend),
		Repo:     repo.FullName,
		Number:   issue.Number,
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

func (e *Engine) HandlePullRequestLabelEvent(ctx context.Context, repo config.RepoConfig, pr PullRequest, action, labelName string) (bool, error) {
	if strings.TrimSpace(strings.ToLower(action)) != "labeled" {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Str("action", action).Str("label", labelName).Msg("pull request label event ignored")
		return false, nil
	}
	if pr.Draft {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Msg("pull request skipped, draft")
		return false, nil
	}
	workflow, agent, role, ok := ParseAILabel(labelName)
	if !ok || workflow != workflowPRReview {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Str("label", labelName).Msg("pull request label skipped")
		return false, nil
	}
	resolvedAgent := e.resolveBackend(agent)
	if resolvedAgent == "" {
		e.logger.Warn().Str("label", labelName).Int("pr_number", pr.Number).Str("repo", repo.FullName).Msg("pr label references unknown agent, skipping")
		return false, nil
	}
	backendCfg, found := e.cfg.AIBackends[resolvedAgent]
	if !found {
		return false, nil
	}
	targets := map[string]map[string]struct{}{resolvedAgent: {}}
	if role == "all" {
		for _, configuredRole := range backendCfg.Agents {
			targets[resolvedAgent][configuredRole] = struct{}{}
		}
	} else {
		if !slices.Contains(backendCfg.Agents, role) {
			e.logger.Warn().Str("label", labelName).Str("agent", resolvedAgent).Str("role", role).Int("pr_number", pr.Number).Str("repo", repo.FullName).Msg("pr label references unsupported role, skipping")
			return false, nil
		}
		targets[resolvedAgent][role] = struct{}{}
	}
	return e.handlePRStateless(ctx, repo, pr, targets)
}

func (e *Engine) handlePRStateless(ctx context.Context, repo config.RepoConfig, pr PullRequest, targets map[string]map[string]struct{}) (bool, error) {
	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("pr_number", pr.Number).
		Logger()

	type agentRoleExecution struct {
		agent    string
		role     string
		workflow string
		runner   ai.Runner
	}
	roleRuns := make([]agentRoleExecution, 0)
	for agent, roles := range targets {
		runner, ok := e.runners[agent]
		if !ok {
			e.logger.Warn().Str("agent", agent).Msg("runner missing for agent, skipping")
			continue
		}
		for role := range roles {
			roleRuns = append(roleRuns, agentRoleExecution{
				agent:    agent,
				role:     role,
				workflow: fmt.Sprintf("%s:%s:%s", workflowPRReview, agent, role),
				runner:   runner,
			})
		}
	}
	if len(roleRuns) == 0 {
		logger.Info().Msg("pull request skipped because no runnable agent roles were resolved")
		return false, nil
	}

	var (
		mu     sync.Mutex
		ranAny bool
	)
	group, groupCtx := errgroup.WithContext(ctx)
	for _, rr := range roleRuns {
		group.Go(func() error {
			prompt := ai.BuildPRReviewPrompt(rr.agent, rr.role, repo.FullName, pr.Number)
			logger.Info().Str("agent", rr.agent).Str("role", rr.role).Msg("invoking ai agent for pr review")
			response, err := rr.runner.Run(groupCtx, ai.Request{
				Workflow: rr.workflow,
				Repo:     repo.FullName,
				Number:   pr.Number,
				Prompt:   prompt,
			})
			if err != nil {
				logger.Error().Err(err).Str("agent", rr.agent).Str("role", rr.role).Msg("ai run failed")
				return nil
			}
			storedCount := len(response.Artifacts)
			logger.Info().Str("agent", rr.agent).Str("role", rr.role).Int("artifacts_stored", storedCount).Msg("pr review completed")
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

func (e *Engine) resolveBackend(agent string) string {
	if strings.TrimSpace(agent) == "" {
		defaultAgent := e.cfg.DefaultConfiguredBackend()
		if defaultAgent == "" {
			e.logger.Error().Msg("no default agent configured; expected one of claude or codex")
		}
		return defaultAgent
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	if _, ok := e.cfg.AIBackends[agent]; !ok {
		return ""
	}
	return agent
}
