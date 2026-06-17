-- 058_self_improvement_feedback_identity_indexes: keep feedback identity
-- columns orthogonal by replacing the legacy table-level unique constraint
-- with source-specific partial indexes.

PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;

DROP INDEX IF EXISTS idx_self_improvement_feedback_workspace_status;
DROP INDEX IF EXISTS idx_self_improvement_feedback_repo;
DROP INDEX IF EXISTS idx_self_improvement_feedback_review_comment;

ALTER TABLE self_improvement_feedback
RENAME TO self_improvement_feedback_legacy_identity_058;

CREATE TABLE self_improvement_feedback (
    id                           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id                 TEXT NOT NULL DEFAULT 'default',
    repo_owner                   TEXT NOT NULL,
    repo_name                    TEXT NOT NULL,
    source_type                  TEXT NOT NULL,
    github_comment_id            INTEGER NOT NULL DEFAULT 0,
    github_review_id             INTEGER NOT NULL DEFAULT 0,
    github_delivery_id           TEXT NOT NULL DEFAULT '',
    source_url                   TEXT NOT NULL DEFAULT '',
    author_login                 TEXT NOT NULL DEFAULT '',
    author_authorized            INTEGER NOT NULL DEFAULT 0,
    issue_number                 INTEGER NOT NULL DEFAULT 0,
    pr_number                    INTEGER NOT NULL DEFAULT 0,
    raw_body                     TEXT NOT NULL DEFAULT '',
    tag                          TEXT NOT NULL DEFAULT '/agents improve',
    file_path                    TEXT NOT NULL DEFAULT '',
    line                         INTEGER NOT NULL DEFAULT 0,
    side                         TEXT NOT NULL DEFAULT '',
    diff_hunk                    TEXT NOT NULL DEFAULT '',
    commit_sha                   TEXT NOT NULL DEFAULT '',
    github_created_at            TIMESTAMP NULL,
    github_updated_at            TIMESTAMP NULL,
    ingested_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    linked_span_id               TEXT NOT NULL DEFAULT '',
    linked_event_id              TEXT NOT NULL DEFAULT '',
    linked_agent_id              TEXT NOT NULL DEFAULT '',
    linked_agent_name            TEXT NOT NULL DEFAULT '',
    linked_prompt_version_id     TEXT NOT NULL DEFAULT '',
    linked_skill_version_ids     TEXT NOT NULL DEFAULT '',
    linked_guardrail_version_ids TEXT NOT NULL DEFAULT '',
    link_confidence              TEXT NOT NULL DEFAULT 'unresolved',
    link_diagnostics             TEXT NOT NULL DEFAULT '',
    status                       TEXT NOT NULL DEFAULT 'new',
    github_review_comment_id     INTEGER NOT NULL DEFAULT 0,
    github_parent_comment_id     INTEGER NOT NULL DEFAULT 0,
    github_pull_request_review_id INTEGER NOT NULL DEFAULT 0
);

INSERT INTO self_improvement_feedback (
    id, workspace_id, repo_owner, repo_name, source_type, github_comment_id,
    github_review_id, github_delivery_id, source_url, author_login,
    author_authorized, issue_number, pr_number, raw_body, tag, file_path,
    line, side, diff_hunk, commit_sha, github_created_at, github_updated_at,
    ingested_at, linked_span_id, linked_event_id, linked_agent_id,
    linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
    linked_guardrail_version_ids, link_confidence, link_diagnostics, status,
    github_review_comment_id, github_parent_comment_id, github_pull_request_review_id
)
SELECT
    id, workspace_id, repo_owner, repo_name, source_type,
    CASE WHEN source_type = 'pull_request_review_comment' THEN 0 ELSE github_comment_id END,
    CASE WHEN source_type = 'pull_request_review_comment' AND github_review_id = github_review_comment_id THEN 0 ELSE github_review_id END,
    github_delivery_id, source_url, author_login, author_authorized,
    issue_number, pr_number, raw_body, tag, file_path, line, side, diff_hunk,
    commit_sha, github_created_at, github_updated_at, ingested_at,
    linked_span_id, linked_event_id, linked_agent_id, linked_agent_name,
    linked_prompt_version_id, linked_skill_version_ids,
    linked_guardrail_version_ids, link_confidence, link_diagnostics, status,
    github_review_comment_id, github_parent_comment_id,
    github_pull_request_review_id
FROM self_improvement_feedback_legacy_identity_058;

DROP TABLE self_improvement_feedback_legacy_identity_058;

PRAGMA writable_schema = ON;

UPDATE sqlite_schema
SET sql = replace(sql, 'self_improvement_feedback_legacy_identity_058', 'self_improvement_feedback')
WHERE type = 'table'
  AND sql LIKE '%self_improvement_feedback_legacy_identity_058%';

PRAGMA writable_schema = OFF;
PRAGMA schema_version = 58;

CREATE UNIQUE INDEX IF NOT EXISTS idx_self_improvement_feedback_legacy_identity
    ON self_improvement_feedback(workspace_id, source_type, github_comment_id, github_review_id, tag)
    WHERE github_review_comment_id = 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_self_improvement_feedback_review_comment
    ON self_improvement_feedback(workspace_id, source_type, github_review_comment_id, tag)
    WHERE github_review_comment_id > 0;

CREATE INDEX IF NOT EXISTS idx_self_improvement_feedback_workspace_status
    ON self_improvement_feedback(workspace_id, status, ingested_at DESC);

CREATE INDEX IF NOT EXISTS idx_self_improvement_feedback_repo
    ON self_improvement_feedback(workspace_id, repo_owner, repo_name, pr_number, issue_number);

PRAGMA foreign_key_check;
PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;
