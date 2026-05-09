-- Workspace and global prompt foundation. This migration keeps the existing
-- inline agent prompt column for compatibility while adding prompt references
-- and workspace ownership that later migrations/API work can make authoritative.

CREATE TABLE IF NOT EXISTS workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO workspaces (id, name, description)
VALUES ('default', 'Default', 'Default operational workspace');

CREATE TABLE IF NOT EXISTS prompts (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

ALTER TABLE agents ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE agents ADD COLUMN prompt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN scope_type TEXT NOT NULL DEFAULT 'workspace';
ALTER TABLE agents ADD COLUMN scope_repo TEXT NOT NULL DEFAULT '';

INSERT OR IGNORE INTO prompts (id, name, description, content)
SELECT 'prompt_' || name, name, 'Migrated prompt for agent ' || name, prompt
FROM agents
WHERE prompt <> '';

UPDATE agents
SET prompt_id = 'prompt_' || name
WHERE prompt_id = '' AND prompt <> '';

-- The original agents.name primary key remains authoritative in this phase;
-- scoped uniqueness becomes effective only after a later table rebuild.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX IF NOT EXISTS idx_agents_workspace ON agents(workspace_id);
CREATE INDEX IF NOT EXISTS idx_agents_prompt ON agents(prompt_id);

ALTER TABLE repos ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default';
-- The original repos.name primary key remains authoritative in this phase;
-- scoped uniqueness becomes effective only after a later table rebuild.
CREATE UNIQUE INDEX IF NOT EXISTS idx_repos_workspace_name ON repos(workspace_id, name);
CREATE INDEX IF NOT EXISTS idx_repos_workspace ON repos(workspace_id);

CREATE TABLE IF NOT EXISTS workspace_guardrails (
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    guardrail_name  TEXT NOT NULL REFERENCES guardrails(name),
    position        INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(workspace_id, guardrail_name)
);

INSERT OR IGNORE INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
SELECT 'default', name, position, enabled
FROM guardrails
WHERE is_builtin = 1;
