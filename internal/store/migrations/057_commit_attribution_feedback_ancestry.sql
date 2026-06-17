-- 057_commit_attribution_feedback_ancestry: direct commit attribution lookups
-- and full PR review comment ancestry for self-improvement feedback.

CREATE UNIQUE INDEX IF NOT EXISTS idx_run_attribution_artifacts_commit
    ON run_attribution_artifacts(workspace_id, repo_owner, repo_name, commit_sha)
    WHERE source_type = 'commit' AND commit_sha <> '';

ALTER TABLE self_improvement_feedback
    ADD COLUMN github_review_comment_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE self_improvement_feedback
    ADD COLUMN github_parent_comment_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE self_improvement_feedback
    ADD COLUMN github_pull_request_review_id INTEGER NOT NULL DEFAULT 0;
