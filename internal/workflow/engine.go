package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/claude"
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
	runner       *claude.Runner
	logger       zerolog.Logger
	lockOwner    string
	lockDuration time.Duration
}

func NewEngine(cfg *config.Config, store *store.Store, githubClient *github.Client, runner *claude.Runner, logger zerolog.Logger) *Engine {
	hostname, _ := os.Hostname()
	owner := fmt.Sprintf("%s:%d", hostname, os.Getpid())
	lockDuration := time.Duration(cfg.Claude.TimeoutSeconds+60) * time.Second
	return &Engine{
		cfg:          cfg,
		store:        store,
		github:       githubClient,
		runner:       runner,
		logger:       logger.With().Str("component", "workflow_engine").Logger(),
		lockOwner:    owner,
		lockDuration: lockDuration,
	}
}

func (e *Engine) HandleIssue(ctx context.Context, repo config.RepoConfig, issue github.Issue) (bool, error) {
	labelGate := repo.IssueLabel
	if labelGate == "" {
		labelGate = e.cfg.Poller.IssueLabel
	}
	if !github.HasLabel(issue.Labels, labelGate) {
		e.logger.Info().Str("repo", repo.FullName).Int("issue_number", issue.Number).Str("required_label", labelGate).Msg("issue skipped, missing label")
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

	run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowIssueRefine, fingerprint)
	if err != nil {
		return false, err
	}
	if !created {
		logger.Info().Msg("workflow run already exists")
		return false, nil
	}

	locked, err := e.store.TryLock(ctx, item.ID, e.lockOwner, e.lockDuration)
	if err != nil {
		return false, err
	}
	if !locked {
		logger.Info().Msg("work item locked by another instance")
		_ = e.store.UpdateWorkflowRunStatus(ctx, run.ID, "skipped", store.SanitizeError(fmt.Errorf("locked by another instance")))
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.store.Unlock(unlockCtx, item.ID, e.lockOwner); err != nil {
			logger.Error().Err(err).Msg("failed to unlock work item")
		}
	}()

	if err := e.enforceQuota(ctx, logger, item.ID, workflowIssueRefine, run.ID); err != nil {
		if errors.Is(err, errQuotaExceeded) {
			return false, nil
		}
		return false, err
	}

	prompt := claude.BuildIssueRefinePrompt(repo.FullName, issue.Number, fingerprint, labelGate)
	logger.Info().Msg("invoking claude for issue refinement")
	response, err := e.runner.Run(ctx, claude.Request{
		Workflow:    workflowIssueRefine,
		Repo:        repo.FullName,
		Number:      issue.Number,
		Fingerprint: fingerprint,
		Prompt:      prompt,
	})
	if err != nil {
		logger.Error().Err(err).Msg("claude run failed")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return false, err
	}

	storedCount, err := e.storeArtifacts(ctx, run.ID, response.Artifacts)
	if err != nil {
		logger.Error().Err(err).Msg("failed to store artifacts")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return false, err
	}
	logger.Info().Int("artifacts_stored", storedCount).Msg("issue refinement completed")

	if err := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "success", nil); err != nil {
		return false, err
	}
	if err := e.store.UpdateWorkItemState(ctx, item.ID, &issue.UpdatedAt, nil); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) HandlePullRequest(ctx context.Context, repo config.RepoConfig, pr github.PullRequest) (bool, error) {
	if pr.Draft {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Msg("pull request skipped, draft")
		return false, nil
	}
	labelGate := repo.PRLabel
	if labelGate == "" {
		labelGate = e.cfg.Poller.PRLabel
	}
	if !github.HasLabel(pr.Labels, labelGate) {
		e.logger.Info().Str("repo", repo.FullName).Int("pr_number", pr.Number).Str("required_label", labelGate).Msg("pull request skipped, missing label")
		return false, nil
	}
	files, err := e.github.ListPullRequestFiles(ctx, repo.FullName, pr.Number, e.cfg.Poller.FileFingerprintLimit)
	if err != nil {
		return false, fmt.Errorf("list pull files: %w", err)
	}
	fingerprint := PRFingerprint(pr, files, e.cfg.Poller.MaxFingerprintBytes)

	item, err := e.store.EnsureWorkItem(ctx, repo.FullName, "pr", pr.Number)
	if err != nil {
		return false, err
	}

	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("pr_number", pr.Number).
		Str("fingerprint", fingerprint).
		Logger()

	run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowPRReview, fingerprint)
	if err != nil {
		return false, err
	}
	if !created {
		logger.Info().Msg("workflow run already exists")
		return false, nil
	}

	locked, err := e.store.TryLock(ctx, item.ID, e.lockOwner, e.lockDuration)
	if err != nil {
		return false, err
	}
	if !locked {
		logger.Info().Msg("work item locked by another instance")
		_ = e.store.UpdateWorkflowRunStatus(ctx, run.ID, "skipped", store.SanitizeError(fmt.Errorf("locked by another instance")))
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.store.Unlock(unlockCtx, item.ID, e.lockOwner); err != nil {
			logger.Error().Err(err).Msg("failed to unlock work item")
		}
	}()

	if err := e.enforceQuota(ctx, logger, item.ID, workflowPRReview, run.ID); err != nil {
		if errors.Is(err, errQuotaExceeded) {
			return false, nil
		}
		return false, err
	}

	prompt := claude.BuildPRReviewPrompt(repo.FullName, pr.Number, fingerprint, labelGate)
	logger.Info().Msg("invoking claude for pr review")
	response, err := e.runner.Run(ctx, claude.Request{
		Workflow:    workflowPRReview,
		Repo:        repo.FullName,
		Number:      pr.Number,
		Fingerprint: fingerprint,
		Prompt:      prompt,
	})
	if err != nil {
		logger.Error().Err(err).Msg("claude run failed")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return false, err
	}

	storedCount, err := e.storeArtifacts(ctx, run.ID, response.Artifacts)
	if err != nil {
		logger.Error().Err(err).Msg("failed to store artifacts")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return false, err
	}
	logger.Info().Int("artifacts_stored", storedCount).Msg("pr review completed")

	if err := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "success", nil); err != nil {
		return false, err
	}
	if err := e.store.UpdateWorkItemState(ctx, item.ID, &pr.UpdatedAt, &pr.Head.SHA); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) enforceQuota(ctx context.Context, logger zerolog.Logger, workItemID int64, workflow string, runID int64) error {
	if e.cfg.Poller.MaxRunsPerHour > 0 {
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflow, time.Now().Add(-1*time.Hour))
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
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflow, time.Now().Add(-24*time.Hour))
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

func (e *Engine) storeArtifacts(ctx context.Context, runID int64, artifacts []claude.Artifact) (int, error) {
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
