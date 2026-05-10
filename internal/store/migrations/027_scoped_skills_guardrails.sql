-- Add catalog visibility scope to skills and guardrails. Existing rows become
-- global catalog items whose stable id matches the old name, preserving every
-- existing agent skill reference and workspace guardrail reference.

-- Normal migrations already have skills from migration 001. The compatibility
-- create keeps older migration tests that seed from later snapshots able to
-- apply this migration in isolation.
CREATE TABLE IF NOT EXISTS skills (
    name   TEXT PRIMARY KEY,
    prompt TEXT NOT NULL
);

CREATE TABLE skills_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT DEFAULT NULL,
    repo         TEXT DEFAULT NULL,
    name         TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    CHECK (workspace_id IS NOT NULL OR repo IS NULL)
);

INSERT INTO skills_new (id, workspace_id, repo, name, prompt)
SELECT name, NULL, NULL, name, prompt
FROM skills;

DROP TABLE skills;
ALTER TABLE skills_new RENAME TO skills;

CREATE UNIQUE INDEX idx_skills_global_name
    ON skills(name)
    WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_workspace_name
    ON skills(workspace_id, name)
    WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_repo_name
    ON skills(workspace_id, repo, name)
    WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_skills_scope ON skills(workspace_id, repo, name);

ALTER TABLE guardrails RENAME TO guardrails_old;

CREATE TABLE guardrails (
  id              TEXT PRIMARY KEY,
  workspace_id    TEXT DEFAULT NULL,
  repo            TEXT DEFAULT NULL,
  name            TEXT    NOT NULL,
  description     TEXT,
  content         TEXT    NOT NULL,
  default_content TEXT,
  is_builtin      INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0, 1)),
  enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  position        INTEGER NOT NULL DEFAULT 100,
  updated_at      TEXT    NOT NULL DEFAULT (datetime('now')),
  CHECK (workspace_id IS NOT NULL OR repo IS NULL)
);

INSERT INTO guardrails (
    id, workspace_id, repo, name, description, content, default_content,
    is_builtin, enabled, position, updated_at
)
SELECT name, NULL, NULL, name, description, content, default_content,
       is_builtin, enabled, position, updated_at
FROM guardrails_old;

ALTER TABLE workspace_guardrails RENAME TO workspace_guardrails_old;

CREATE UNIQUE INDEX idx_guardrails_global_name
    ON guardrails(name)
    WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_workspace_name
    ON guardrails(workspace_id, name)
    WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_repo_name
    ON guardrails(workspace_id, repo, name)
    WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_guardrails_scope ON guardrails(workspace_id, repo, name);

CREATE TABLE workspace_guardrails_new (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- guardrail_name stores guardrails.id after this migration. The legacy
    -- column name is kept to avoid a second table-wide rename in this PR.
    guardrail_name TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (workspace_id, guardrail_name)
);

INSERT INTO workspace_guardrails_new (workspace_id, guardrail_name, position, enabled)
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails_old;

DROP TABLE workspace_guardrails_old;
ALTER TABLE workspace_guardrails_new RENAME TO workspace_guardrails;

DROP TABLE guardrails_old;
