-- Remove the legacy inline agent prompt column. Prompt bodies now live only in
-- the prompt catalog; agents keep prompt_id references.

CREATE TABLE IF NOT EXISTS workspaces (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    description  TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    runner_image TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO workspaces (id, name, description, runner_image)
VALUES ('default', 'Default', 'Default operational workspace', '');

CREATE TABLE IF NOT EXISTS agents (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL DEFAULT 'auto',
    model          TEXT NOT NULL DEFAULT '',
    skills         TEXT NOT NULL DEFAULT '[]',
    prompt         TEXT NOT NULL DEFAULT '',
    prompt_id      TEXT NOT NULL DEFAULT '',
    scope_type     TEXT NOT NULL DEFAULT 'workspace',
    scope_repo     TEXT NOT NULL DEFAULT '',
    allow_prs      INTEGER NOT NULL DEFAULT 0,
    allow_dispatch INTEGER NOT NULL DEFAULT 0,
    can_dispatch   TEXT NOT NULL DEFAULT '[]',
    description    TEXT NOT NULL DEFAULT '',
    allow_memory   INTEGER NOT NULL DEFAULT 1,
    UNIQUE(workspace_id, name)
);

CREATE TABLE agents_new (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL DEFAULT 'auto',
    model          TEXT NOT NULL DEFAULT '',
    skills         TEXT NOT NULL DEFAULT '[]',
    prompt_id      TEXT NOT NULL DEFAULT '',
    scope_type     TEXT NOT NULL DEFAULT 'workspace',
    scope_repo     TEXT NOT NULL DEFAULT '',
    allow_prs      INTEGER NOT NULL DEFAULT 0,
    allow_dispatch INTEGER NOT NULL DEFAULT 0,
    can_dispatch   TEXT NOT NULL DEFAULT '[]',
    description    TEXT NOT NULL DEFAULT '',
    allow_memory   INTEGER NOT NULL DEFAULT 1,
    UNIQUE(workspace_id, name)
);

INSERT INTO agents_new (
    id, workspace_id, name, backend, model, skills, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch, can_dispatch,
    description, allow_memory
)
SELECT
    id, workspace_id, name, backend, model, skills, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch, can_dispatch,
    description, allow_memory
FROM agents;

DROP TABLE agents;
ALTER TABLE agents_new RENAME TO agents;

CREATE UNIQUE INDEX idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX idx_agents_workspace ON agents(workspace_id);
CREATE INDEX idx_agents_prompt ON agents(prompt_id);
