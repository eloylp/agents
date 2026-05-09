-- Make workspace identity authoritative for agents and repos. Earlier
-- migrations added workspace_id plus composite unique indexes, but the legacy
-- name primary keys still prevented common names from being reused across
-- workspaces. Rebuild the dependent tables so agents are keyed by stable id
-- and repos/bindings/memory use workspace-qualified names.

CREATE TABLE agents_new (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL DEFAULT 'auto',
    model          TEXT NOT NULL DEFAULT '',
    skills         TEXT NOT NULL DEFAULT '[]',
    prompt         TEXT NOT NULL,
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
    id, workspace_id, name, backend, model, skills, prompt, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch, can_dispatch,
    description, allow_memory
)
SELECT
    COALESCE(NULLIF(id, ''), 'agent_' || lower(hex(randomblob(16)))),
    COALESCE(NULLIF(workspace_id, ''), 'default'),
    name, backend, model, skills, prompt, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch, can_dispatch,
    description, allow_memory
FROM agents;

CREATE TABLE repos_new (
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id),
    name         TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(workspace_id, name)
);

INSERT INTO repos_new (workspace_id, name, enabled)
SELECT COALESCE(NULLIF(workspace_id, ''), 'default'), name, enabled
FROM repos;

CREATE TABLE bindings_copy (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repo         TEXT NOT NULL,
    agent        TEXT NOT NULL,
    labels       TEXT NOT NULL,
    events       TEXT NOT NULL,
    cron         TEXT NOT NULL,
    enabled      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bindings (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    repo    TEXT NOT NULL REFERENCES repos(name),
    agent   TEXT NOT NULL REFERENCES agents(name),
    labels  TEXT NOT NULL DEFAULT '[]',
    events  TEXT NOT NULL DEFAULT '[]',
    cron    TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1
);

INSERT INTO bindings_copy (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT b.id, COALESCE(NULLIF(r.workspace_id, ''), 'default'), b.repo, b.agent, b.labels, b.events, b.cron, b.enabled
FROM bindings b
LEFT JOIN repos r ON r.name = b.repo;

CREATE TABLE memory_copy (
    workspace_id TEXT NOT NULL,
    agent        TEXT NOT NULL,
    repo         TEXT NOT NULL,
    content      TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

INSERT INTO memory_copy (workspace_id, agent, repo, content, updated_at)
SELECT COALESCE(NULLIF(workspace_id, ''), 'default'), agent, repo, content, updated_at
FROM memory;

CREATE TABLE graph_layouts_copy (
    id         INTEGER PRIMARY KEY,
    scope      TEXT NOT NULL,
    node_kind  TEXT NOT NULL,
    node_id    TEXT NOT NULL,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO graph_layouts_copy (id, scope, node_kind, node_id, x, y, updated_at)
SELECT id, scope, node_kind, node_id, x, y, updated_at
FROM graph_layouts;

DROP TABLE graph_layouts;
DROP TABLE bindings;
DROP TABLE memory;
DROP TABLE agents;
DROP TABLE repos;

ALTER TABLE agents_new RENAME TO agents;
ALTER TABLE repos_new RENAME TO repos;

CREATE UNIQUE INDEX idx_agents_id ON agents(id);
CREATE UNIQUE INDEX idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX idx_agents_workspace ON agents(workspace_id);
CREATE INDEX idx_agents_prompt ON agents(prompt_id);

CREATE UNIQUE INDEX idx_repos_workspace_name ON repos(workspace_id, name);
CREATE INDEX idx_repos_workspace ON repos(workspace_id);

CREATE TABLE memory (
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    agent        TEXT NOT NULL,
    repo         TEXT NOT NULL,
    content      TEXT NOT NULL,
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (workspace_id, agent, repo)
);

INSERT INTO memory (workspace_id, agent, repo, content, updated_at)
SELECT workspace_id, agent, repo, content, updated_at
FROM memory_copy;

CREATE INDEX idx_memory_workspace ON memory(workspace_id);

CREATE TABLE bindings (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default',
    repo         TEXT NOT NULL,
    agent        TEXT NOT NULL,
    labels       TEXT NOT NULL DEFAULT '[]',
    events       TEXT NOT NULL DEFAULT '[]',
    cron         TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, agent) REFERENCES agents(workspace_id, name)
);

INSERT INTO bindings (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT id, workspace_id, repo, agent, labels, events, cron, enabled
FROM bindings_copy;

CREATE INDEX idx_bindings_workspace_repo ON bindings(workspace_id, repo);
CREATE INDEX idx_bindings_workspace_agent ON bindings(workspace_id, agent);

CREATE TABLE graph_layouts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    scope      TEXT NOT NULL DEFAULT 'workspace:default',
    node_kind  TEXT NOT NULL,
    node_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(scope, node_kind, node_id)
);

INSERT INTO graph_layouts (id, scope, node_kind, node_id, x, y, updated_at)
SELECT id, scope, node_kind, node_id, x, y, updated_at
FROM graph_layouts_copy;

DROP TABLE bindings_copy;
DROP TABLE memory_copy;
DROP TABLE graph_layouts_copy;
