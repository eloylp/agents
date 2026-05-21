-- Guardrails are selected at workspace level and rendered for every run in
-- that workspace. Prompts and skills may still be repo-scoped, but guardrails
-- deliberately do not carry a repo dimension.

CREATE TABLE workspace_guardrails_copy_034 AS
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails;

DROP TABLE workspace_guardrails;

ALTER TABLE guardrails RENAME TO guardrails_old;

CREATE TABLE guardrails (
  id              TEXT PRIMARY KEY,
  ref             TEXT NOT NULL UNIQUE,
  workspace_id    TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  name            TEXT    NOT NULL,
  description     TEXT,
  content         TEXT    NOT NULL,
  default_content TEXT,
  is_builtin      INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0, 1)),
  enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  position        INTEGER NOT NULL DEFAULT 100,
  updated_at      TEXT    NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO guardrails (
    id, ref, workspace_id, name, description, content, default_content,
    is_builtin, enabled, position, updated_at
)
SELECT id, ref, workspace_id, name, description, content, default_content,
       is_builtin, enabled, position, updated_at
FROM guardrails_old;

DROP TABLE guardrails_old;

CREATE UNIQUE INDEX idx_guardrails_global_name
    ON guardrails(name)
    WHERE workspace_id IS NULL;
CREATE UNIQUE INDEX idx_guardrails_workspace_name
    ON guardrails(workspace_id, name)
    WHERE workspace_id IS NOT NULL;
CREATE INDEX idx_guardrails_scope ON guardrails(workspace_id, name);

CREATE TABLE workspace_guardrails (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    guardrail_name TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (workspace_id, guardrail_name)
);

INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails_copy_034;

DROP TABLE workspace_guardrails_copy_034;
