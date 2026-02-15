package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/github"
	"github.com/eloylp/agents/internal/store"
)

const (
	workflowIssueRefine = "issue_refine"
	workflowPRReview    = "pr_review"
)

var errQuotaExceeded = errors.New("quota exceeded")

type Engine struct {
	cfg          *config.Config
	store        *store.Store
	github       *github.Client
	runners      map[string]ai.Runner
	logger       zerolog.Logger
	lockOwner    string
	lockDuration time.Duration
}

func NewEngine(cfg *config.Config, store *store.Store, githubClient *github.Client, runners map[string]ai.Runner, logger zerolog.Logger) *Engine {
	hostname, _ := os.Hostname()
	owner := fmt.Sprintf("%s:%d", hostname, os.Getpid())
	lockDuration := time.Duration(cfg.MaxAgentTimeoutSeconds()+60) * time.Second
	return &Engine{
		cfg:          cfg,
		store:        store,
		github:       githubClient,
		runners:      runners,
		logger:       logger.With().Str("component", "workflow_engine").Logger(),
		lockOwner:    owner,
		lockDuration: lockDuration,
	}
}

func (e *Engine) HandleIssue(ctx context.Context, repo config.RepoConfig, issue github.Issue) (bool, error) {
	agents := make([]string, 0, 2)
	for _, label := range issue.Labels {
		workflow, agent, _, ok := ParseAILabel(label.Name)
		if !ok || workflow != workflowIssueRefine {
			continue
		}
		resolved := e.resolveAgent(agent)
		if resolved == "" {
			e.logger.Warn().Str("label", label.Name).Int("issue_number", issue.Number).Str("repo", repo.FullName).Msg("issue label references unknown agent, skipping")
			continue
		}
		agents = append(agents, resolved)
	}
	agents = uniqueStrings(agents)
	if len(agents) == 0 {
		e.logger.Info().Str("repo", repo.FullName).Int("issue_number", issue.Number).Msg("issue skipped, missing ai refine label")
		return false, nil
	}

	comments, err := e.github.ListIssueComments(ctx, repo.FullName, issue.Number, e.cfg.Poller.CommentFingerprintLimit)
	if err != nil {
		return false, fmt.Errorf("list issue comments: %w", err)
	}
	fingerprint := IssueFingerprint(issue, comments, e.cfg.Poller.MaxFingerprintBytes)

	item, err := e.store.EnsureWorkItem(ctx, repo.FullName, "issue", issue.Number)
	if err != nil {
		return false, err
	}

	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("issue_number", issue.Number).
		Str("fingerprint", fingerprint).
		Logger()

	locked, err := e.store.TryLock(ctx, item.ID, e.lockOwner, e.lockDuration)
	if err != nil {
		return false, err
	}
	if !locked {
		logger.Info().Msg("work item locked by another instance")
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.store.Unlock(unlockCtx, item.ID, e.lockOwner); err != nil {
			logger.Error().Err(err).Msg("failed to unlock work item")
		}
	}()

	ranAny := false
	for _, agent := range agents {
		workflowName := fmt.Sprintf("%s:%s", workflowIssueRefine, agent)
		run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowName, fingerprint)
		if err != nil {
			return false, err
		}
		if !created {
			logger.Info().Str("agent", agent).Msg("workflow run already exists")
			continue
		}
		if err := e.enforceQuota(ctx, logger, item.ID, workflowIssueRefine, run.ID); err != nil {
			if errors.Is(err, errQuotaExceeded) {
				continue
			}
			return false, err
		}
		runner, ok := e.runners[agent]
		if !ok {
			logger.Warn().Str("agent", agent).Msg("runner missing for agent, skipping")
			_ = e.store.UpdateWorkflowRunStatus(ctx, run.ID, "skipped", store.SanitizeError(fmt.Errorf("runner missing for agent %s", agent)))
			continue
		}
		prompt := ai.BuildIssueRefinePrompt(agent, repo.FullName, issue.Number, fingerprint)
		logger.Info().Str("agent", agent).Msg("invoking ai agent for issue refinement")
		response, err := runner.Run(ctx, ai.Request{
			Workflow:    workflowName,
			Repo:        repo.FullName,
			Number:      issue.Number,
			Fingerprint: fingerprint,
			Prompt:      prompt,
		})
		if err != nil {
			logger.Error().Err(err).Str("agent", agent).Msg("ai run failed")
			updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
			if updateErr != nil {
				logger.Error().Err(updateErr).Msg("failed to update workflow run")
			}
			continue
		}
		storedCount, err := e.storeArtifacts(ctx, run.ID, response.Artifacts)
		if err != nil {
			logger.Error().Err(err).Str("agent", agent).Msg("failed to store artifacts")
			updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
			if updateErr != nil {
				logger.Error().Err(updateErr).Msg("failed to update workflow run")
			}
			continue
		}
		logger.Info().Str("agent", agent).Int("artifacts_stored", storedCount).Msg("issue refinement completed")
		if err := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "success", nil); err != nil {
			return false, err
		}
		ranAny = true
	}

	if ranAny {
		if err := e.store.UpdateWorkItemState(ctx, item.ID, &issue.UpdatedAt, nil); err != nil {
			return false, err
		}
	}
	return ranAny, nil
}

func (e *Engine) HandlePullRequest(ctx context.Context, repo config.RepoConfig, pr github.PullRequest) (bool, error) {
	if pr.Draft {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Msg("pull request skipped, draft")
		return false, nil
	}
	targets := map[string]map[string]struct{}{}
	for _, label := range pr.Labels {
		workflow, agent, role, ok := ParseAILabel(label.Name)
		if !ok || workflow != workflowPRReview {
			continue
		}
		resolvedAgent := e.resolveAgent(agent)
		if resolvedAgent == "" {
			e.logger.Warn().Str("label", label.Name).Int("pr_number", pr.Number).Str("repo", repo.FullName).Msg("pr label references unknown agent, skipping")
			continue
		}
		agentCfg, found := e.cfg.Agents[resolvedAgent]
		if !found {
			continue
		}
		if targets[resolvedAgent] == nil {
			targets[resolvedAgent] = map[string]struct{}{}
		}
		if role == "all" {
			for _, configuredRole := range agentCfg.Roles {
				targets[resolvedAgent][configuredRole] = struct{}{}
			}
			continue
		}
		if !slices.Contains(agentCfg.Roles, role) {
			e.logger.Warn().Str("label", label.Name).Str("agent", resolvedAgent).Str("role", role).Int("pr_number", pr.Number).Str("repo", repo.FullName).Msg("pr label references unsupported role, skipping")
			continue
		}
		targets[resolvedAgent][role] = struct{}{}
	}
	if len(targets) == 0 {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Msg("pull request skipped, missing ai review label")
		return false, nil
	}

	files, err := e.github.ListPullRequestFiles(ctx, repo.FullName, pr.Number, e.cfg.Poller.FileFingerprintLimit)
	if err != nil {
		return false, fmt.Errorf("list pull files: %w", err)
	}

	item, err := e.store.EnsureWorkItem(ctx, repo.FullName, "pr", pr.Number)
	if err != nil {
		return false, err
	}

	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("pr_number", pr.Number).
		Logger()

	locked, err := e.store.TryLock(ctx, item.ID, e.lockOwner, e.lockDuration)
	if err != nil {
		return false, err
	}
	if !locked {
		logger.Info().Msg("work item locked by another instance")
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.store.Unlock(unlockCtx, item.ID, e.lockOwner); err != nil {
			logger.Error().Err(err).Msg("failed to unlock work item")
		}
	}()

	type agentRoleExecution struct {
		agent       string
		role        string
		workflow    string
		fingerprint string
		runner      ai.Runner
		runID       int64
	}
	roleRuns := make([]agentRoleExecution, 0)
	for agent, roles := range targets {
		runner, ok := e.runners[agent]
		if !ok {
			e.logger.Warn().Str("agent", agent).Msg("runner missing for agent, skipping")
			continue
		}
		for role := range roles {
			fingerprint := PRFingerprint(pr, role, files, e.cfg.Poller.MaxFingerprintBytes)
			workflowName := fmt.Sprintf("%s:%s:%s", workflowPRReview, agent, role)
			run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowName, fingerprint)
			if err != nil {
				return false, err
			}
			if !created {
				logger.Info().Str("agent", agent).Str("role", role).Str("fingerprint", fingerprint).Msg("workflow run already exists")
				continue
			}
			if err := e.enforceQuota(ctx, logger, item.ID, workflowPRReview, run.ID); err != nil {
				if errors.Is(err, errQuotaExceeded) {
					continue
				}
				return false, err
			}
			roleRuns = append(roleRuns, agentRoleExecution{agent: agent, role: role, workflow: workflowName, fingerprint: fingerprint, runner: runner, runID: run.ID})
		}
	}
	if len(roleRuns) == 0 {
		return false, nil
	}

	var (
		mu     sync.Mutex
		ranAny bool
	)
	group, groupCtx := errgroup.WithContext(ctx)
	for _, rr := range roleRuns {
		rr := rr
		group.Go(func() error {
			prompt := ai.BuildPRReviewPrompt(rr.agent, rr.role, repo.FullName, pr.Number, rr.fingerprint)
			logger.Info().Str("agent", rr.agent).Str("role", rr.role).Msg("invoking ai agent for pr review")
			response, err := rr.runner.Run(groupCtx, ai.Request{
				Workflow:    rr.workflow,
				Repo:        repo.FullName,
				Number:      pr.Number,
				Fingerprint: rr.fingerprint,
				Prompt:      prompt,
			})
			if err != nil {
				logger.Error().Err(err).Str("agent", rr.agent).Str("role", rr.role).Msg("ai run failed")
				updateErr := e.store.UpdateWorkflowRunStatus(ctx, rr.runID, "failed", store.SanitizeError(err))
				if updateErr != nil {
					logger.Error().Err(updateErr).Msg("failed to update workflow run")
				}
				return nil
			}
			storedCount, err := e.storeArtifacts(ctx, rr.runID, response.Artifacts)
			if err != nil {
				logger.Error().Err(err).Str("agent", rr.agent).Str("role", rr.role).Msg("failed to store artifacts")
				updateErr := e.store.UpdateWorkflowRunStatus(ctx, rr.runID, "failed", store.SanitizeError(err))
				if updateErr != nil {
					logger.Error().Err(updateErr).Msg("failed to update workflow run")
				}
				return nil
			}
			logger.Info().Str("agent", rr.agent).Str("role", rr.role).Int("artifacts_stored", storedCount).Msg("pr review completed")
			if err := e.store.UpdateWorkflowRunStatus(ctx, rr.runID, "success", nil); err != nil {
				return err
			}
			mu.Lock()
			ranAny = true
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return false, err
	}
	if ranAny {
		if err := e.store.UpdateWorkItemState(ctx, item.ID, &pr.UpdatedAt, &pr.Head.SHA); err != nil {
			return false, err
		}
	}
	return ranAny, nil
}

func (e *Engine) resolveAgent(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return e.cfg.DefaultAgent
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	if _, ok := e.cfg.Agents[agent]; !ok {
		return ""
	}
	return agent
}

func (e *Engine) enforceQuota(ctx context.Context, logger zerolog.Logger, workItemID int64, workflowPrefix string, runID int64) error {
	if e.cfg.Poller.MaxRunsPerHour > 0 {
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflowPrefix, time.Now().Add(-1*time.Hour))
		if err != nil {
			return err
		}
		if count > e.cfg.Poller.MaxRunsPerHour {
			logger.Warn().Msg("workflow run skipped due to hourly quota")
			if err := e.store.UpdateWorkflowRunStatus(ctx, runID, "skipped", store.SanitizeError(fmt.Errorf("hourly quota exceeded"))); err != nil {
				return err
			}
			return errQuotaExceeded
		}
	}
	if e.cfg.Poller.MaxRunsPerDay > 0 {
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflowPrefix, time.Now().Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if count > e.cfg.Poller.MaxRunsPerDay {
			logger.Warn().Msg("workflow run skipped due to daily quota")
			if err := e.store.UpdateWorkflowRunStatus(ctx, runID, "skipped", store.SanitizeError(fmt.Errorf("daily quota exceeded"))); err != nil {
				return err
			}
			return errQuotaExceeded
		}
	}
	return nil
}

func (e *Engine) storeArtifacts(ctx context.Context, runID int64, artifacts []ai.Artifact) (int, error) {
	maxPosts := e.cfg.Poller.MaxPostsPerRun
	stored := 0
	for i, artifact := range artifacts {
		if maxPosts > 0 && i >= maxPosts {
			break
		}
		record := store.Artifact{
			WorkflowRunID: runID,
			ArtifactType:  artifact.Type,
			PartKey:       artifact.PartKey,
			GitHubID:      artifact.GitHubID,
			URL:           artifact.URL,
		}
		created, err := e.store.RecordArtifact(ctx, record)
		if err != nil {
			return stored, err
		}
		if created {
			stored++
		}
	}
	return stored, nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
