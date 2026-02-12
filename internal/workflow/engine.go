package workflow

import (
	"context"
	"fmt"
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

type Engine struct {
	cfg    *config.Config
	store  *store.Store
	github *github.Client
	runner *claude.Runner
	logger zerolog.Logger
}

func NewEngine(cfg *config.Config, store *store.Store, githubClient *github.Client, runner *claude.Runner, logger zerolog.Logger) *Engine {
	return &Engine{
		cfg:    cfg,
		store:  store,
		github: githubClient,
		runner: runner,
		logger: logger.With().Str("component", "workflow_engine").Logger(),
	}
}

func (e *Engine) HandleIssue(ctx context.Context, repo config.RepoConfig, issue github.Issue) error {
	labelGate := repo.IssueLabel
	if labelGate == "" {
		labelGate = e.cfg.Poller.IssueLabel
	}
	if !github.HasLabel(issue.Labels, labelGate) {
		return nil
	}
	comments, err := e.github.ListIssueComments(ctx, repo.FullName, issue.Number, e.cfg.Poller.CommentFingerprintLimit)
	if err != nil {
		return fmt.Errorf("list issue comments: %w", err)
	}
	fingerprint := IssueFingerprint(issue, comments, e.cfg.Poller.MaxFingerprintBytes)

	item, err := e.store.EnsureWorkItem(ctx, repo.FullName, "issue", issue.Number)
	if err != nil {
		return err
	}

	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("issue_number", issue.Number).
		Str("fingerprint", fingerprint).
		Logger()

	run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowIssueRefine, fingerprint)
	if err != nil {
		return err
	}
	if !created {
		logger.Info().Msg("workflow run already exists")
		return nil
	}

	if err := e.enforceQuota(ctx, logger, item.ID, workflowIssueRefine, run.ID); err != nil {
		return err
	}

	prompt := claude.BuildIssueRefinePrompt(repo.FullName, issue.Number, fingerprint, labelGate)
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
		return err
	}

	storedCount, err := e.storeArtifacts(ctx, run.ID, response.Artifacts)
	if err != nil {
		logger.Error().Err(err).Msg("failed to store artifacts")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return err
	}
	logger.Info().Int("artifacts_stored", storedCount).Msg("issue refinement completed")

	if err := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "success", nil); err != nil {
		return err
	}
	if err := e.store.UpdateWorkItemState(ctx, item.ID, &issue.UpdatedAt, nil); err != nil {
		return err
	}
	return nil
}

func (e *Engine) HandlePullRequest(ctx context.Context, repo config.RepoConfig, pr github.PullRequest) error {
	if pr.Draft {
		return nil
	}
	labelGate := repo.PRLabel
	if labelGate == "" {
		labelGate = e.cfg.Poller.PRLabel
	}
	if !github.HasLabel(pr.Labels, labelGate) {
		return nil
	}
	files, err := e.github.ListPullRequestFiles(ctx, repo.FullName, pr.Number, e.cfg.Poller.FileFingerprintLimit)
	if err != nil {
		return fmt.Errorf("list pull files: %w", err)
	}
	fingerprint := PRFingerprint(pr, files, e.cfg.Poller.MaxFingerprintBytes)

	item, err := e.store.EnsureWorkItem(ctx, repo.FullName, "pr", pr.Number)
	if err != nil {
		return err
	}

	logger := e.logger.With().
		Str("repo", repo.FullName).
		Int("pr_number", pr.Number).
		Str("fingerprint", fingerprint).
		Logger()

	run, created, err := e.store.CreateWorkflowRun(ctx, item.ID, workflowPRReview, fingerprint)
	if err != nil {
		return err
	}
	if !created {
		logger.Info().Msg("workflow run already exists")
		return nil
	}

	if err := e.enforceQuota(ctx, logger, item.ID, workflowPRReview, run.ID); err != nil {
		return err
	}

	prompt := claude.BuildPRReviewPrompt(repo.FullName, pr.Number, fingerprint, labelGate)
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
		return err
	}

	storedCount, err := e.storeArtifacts(ctx, run.ID, response.Artifacts)
	if err != nil {
		logger.Error().Err(err).Msg("failed to store artifacts")
		updateErr := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "failed", store.SanitizeError(err))
		if updateErr != nil {
			logger.Error().Err(updateErr).Msg("failed to update workflow run")
		}
		return err
	}
	logger.Info().Int("artifacts_stored", storedCount).Msg("pr review completed")

	if err := e.store.UpdateWorkflowRunStatus(ctx, run.ID, "success", nil); err != nil {
		return err
	}
	if err := e.store.UpdateWorkItemState(ctx, item.ID, &pr.UpdatedAt, &pr.Head.SHA); err != nil {
		return err
	}
	return nil
}

func (e *Engine) enforceQuota(ctx context.Context, logger zerolog.Logger, workItemID int64, workflow string, runID int64) error {
	if e.cfg.Poller.MaxRunsPerHour > 0 {
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflow, time.Now().Add(-1*time.Hour))
		if err != nil {
			return err
		}
		if count >= e.cfg.Poller.MaxRunsPerHour {
			logger.Warn().Msg("workflow run skipped due to hourly quota")
			return e.store.UpdateWorkflowRunStatus(ctx, runID, "skipped", store.SanitizeError(fmt.Errorf("hourly quota exceeded")))
		}
	}
	if e.cfg.Poller.MaxRunsPerDay > 0 {
		count, err := e.store.CountWorkflowRunsSince(ctx, workItemID, workflow, time.Now().Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if count >= e.cfg.Poller.MaxRunsPerDay {
			logger.Warn().Msg("workflow run skipped due to daily quota")
			return e.store.UpdateWorkflowRunStatus(ctx, runID, "skipped", store.SanitizeError(fmt.Errorf("daily quota exceeded")))
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
