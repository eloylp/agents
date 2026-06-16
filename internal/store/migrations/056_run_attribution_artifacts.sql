-- 056_run_attribution_artifacts: narrow reverse-index from GitHub artifacts
-- (comments, reviews, review comments, commit trailers) to the span_id that
-- produced them. Keeps run_attributions as the canonical run-fact table; this
-- table only holds the GitHub object identity → span_id mapping so the daemon
-- can resolve inline PR review feedback without requiring the human to paste
-- or preserve attribution metadata.

CREATE TABLE IF NOT EXISTS run_attribution_artifacts (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id                TEXT NOT NULL DEFAULT 'default',
    repo_owner                  TEXT NOT NULL,
    repo_name                   TEXT NOT NULL,
    issue_or_pr_number          INTEGER NOT NULL DEFAULT 0,
    source_type                 TEXT NOT NULL,
    github_comment_id           INTEGER NOT NULL DEFAULT 0,
    github_review_id            INTEGER NOT NULL DEFAULT 0,
    github_review_comment_id    INTEGER NOT NULL DEFAULT 0,
    github_parent_comment_id    INTEGER NOT NULL DEFAULT 0,
    github_delivery_id          TEXT NOT NULL DEFAULT '',
    source_url                  TEXT NOT NULL DEFAULT '',
    author_login                TEXT NOT NULL DEFAULT '',
    file_path                   TEXT NOT NULL DEFAULT '',
    line                        INTEGER NOT NULL DEFAULT 0,
    side                        TEXT NOT NULL DEFAULT '',
    commit_sha                  TEXT NOT NULL DEFAULT '',
    span_id                     TEXT NOT NULL,
    metadata_json               TEXT NOT NULL DEFAULT '',
    github_created_at           TIMESTAMP NULL,
    github_updated_at           TIMESTAMP NULL,
    observed_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Lookup by comment identity (issue_comment and PR review comments via comment_id)
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_attribution_artifacts_comment
    ON run_attribution_artifacts(workspace_id, repo_owner, repo_name, source_type, github_comment_id)
    WHERE github_comment_id > 0;

-- Lookup by PR review identity (pull_request_review via review_id)
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_attribution_artifacts_review
    ON run_attribution_artifacts(workspace_id, repo_owner, repo_name, github_review_id)
    WHERE github_review_id > 0 AND github_review_comment_id = 0;

-- Lookup by PR review comment identity (pull_request_review_comment via review_comment_id)
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_attribution_artifacts_review_comment
    ON run_attribution_artifacts(workspace_id, repo_owner, repo_name, github_review_comment_id)
    WHERE github_review_comment_id > 0;

-- PR/thread context lookup for conservative fallback
CREATE INDEX IF NOT EXISTS idx_run_attribution_artifacts_pr_context
    ON run_attribution_artifacts(workspace_id, repo_owner, repo_name, issue_or_pr_number, file_path, commit_sha, observed_at);
