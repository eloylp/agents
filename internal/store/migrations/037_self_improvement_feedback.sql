-- 037_self_improvement_feedback: immutable evidence records for tagged
-- maintainer feedback that later self-improvement recommendation runs analyze.

CREATE TABLE IF NOT EXISTS self_improvement_feedback (
    id                         INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id               TEXT NOT NULL DEFAULT 'default',
    repo_owner                 TEXT NOT NULL,
    repo_name                  TEXT NOT NULL,
    source_type                TEXT NOT NULL,
    github_comment_id          INTEGER NOT NULL DEFAULT 0,
    github_review_id           INTEGER NOT NULL DEFAULT 0,
    github_delivery_id         TEXT NOT NULL DEFAULT '',
    source_url                 TEXT NOT NULL DEFAULT '',
    author_login               TEXT NOT NULL DEFAULT '',
    author_authorized          INTEGER NOT NULL DEFAULT 0,
    issue_number               INTEGER NOT NULL DEFAULT 0,
    pr_number                  INTEGER NOT NULL DEFAULT 0,
    raw_body                   TEXT NOT NULL DEFAULT '',
    tag                        TEXT NOT NULL DEFAULT '#ai_improvement',
    file_path                  TEXT NOT NULL DEFAULT '',
    line                       INTEGER NOT NULL DEFAULT 0,
    side                       TEXT NOT NULL DEFAULT '',
    diff_hunk                  TEXT NOT NULL DEFAULT '',
    commit_sha                 TEXT NOT NULL DEFAULT '',
    github_created_at          TIMESTAMP NULL,
    github_updated_at          TIMESTAMP NULL,
    ingested_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    linked_span_id             TEXT NOT NULL DEFAULT '',
    linked_event_id            TEXT NOT NULL DEFAULT '',
    linked_agent_id            TEXT NOT NULL DEFAULT '',
    linked_agent_name          TEXT NOT NULL DEFAULT '',
    linked_prompt_version_id   TEXT NOT NULL DEFAULT '',
    linked_skill_version_ids   TEXT NOT NULL DEFAULT '',
    linked_guardrail_version_ids TEXT NOT NULL DEFAULT '',
    link_confidence            TEXT NOT NULL DEFAULT 'unresolved',
    link_diagnostics           TEXT NOT NULL DEFAULT '',
    status                     TEXT NOT NULL DEFAULT 'new',
    UNIQUE(workspace_id, source_type, github_comment_id, github_review_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_self_improvement_feedback_workspace_status
    ON self_improvement_feedback(workspace_id, status, ingested_at DESC);

CREATE INDEX IF NOT EXISTS idx_self_improvement_feedback_repo
    ON self_improvement_feedback(workspace_id, repo_owner, repo_name, pr_number, issue_number);
