-- 036_run_attributions: durable lookup table for linking GitHub feedback back
-- to the agent run that produced the public artifact being reviewed.

CREATE TABLE IF NOT EXISTS run_attributions (
    span_id               TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL DEFAULT 'default',
    repo_owner            TEXT NOT NULL,
    repo_name             TEXT NOT NULL,
    issue_or_pr_number    INTEGER NOT NULL DEFAULT 0,
    event_id              TEXT NOT NULL DEFAULT '',
    event_queue_id        INTEGER NOT NULL DEFAULT 0,
    agent_id              TEXT NOT NULL DEFAULT '',
    agent_name            TEXT NOT NULL,
    backend_id            TEXT NOT NULL DEFAULT '',
    backend_name          TEXT NOT NULL,
    prompt_version_id     TEXT NULL,
    prompt_ref            TEXT NOT NULL DEFAULT '',
    skill_version_ids     TEXT NOT NULL DEFAULT '',
    guardrail_version_ids TEXT NOT NULL DEFAULT '',
    head_sha              TEXT NOT NULL DEFAULT '',
    branch                TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_run_attributions_repo_number
    ON run_attributions(workspace_id, repo_owner, repo_name, issue_or_pr_number, created_at);

CREATE INDEX IF NOT EXISTS idx_run_attributions_head_sha
    ON run_attributions(workspace_id, repo_owner, repo_name, head_sha);
