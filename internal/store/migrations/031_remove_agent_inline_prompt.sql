-- Remove the legacy inline agent prompt column. Earlier migrations own the
-- workspaces and agents table creation; this migration only rebuilds agents
-- without the prompt column.
--
-- bindings and graph_layouts both reference agents, so rebuild them around the
-- parent table rebuild instead of dropping agents while FK children still exist.

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

INSERT INTO bindings_copy (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT id, workspace_id, repo, agent, labels, events, cron, enabled
FROM bindings;

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

DROP TABLE graph_layouts;
DROP TABLE bindings;
DROP TABLE agents;
ALTER TABLE agents_new RENAME TO agents;

CREATE UNIQUE INDEX idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX idx_agents_workspace ON agents(workspace_id);
CREATE INDEX idx_agents_prompt ON agents(prompt_id);

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
DROP TABLE graph_layouts_copy;
