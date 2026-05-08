-- Stable agent identities for UI layout state. Names remain the domain key
-- for dispatch/config semantics, but graph layout must not depend on a
-- user-editable label.
ALTER TABLE agents ADD COLUMN id TEXT;

UPDATE agents
SET id = 'agent_' || lower(hex(randomblob(16)))
WHERE id IS NULL OR id = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_id ON agents(id);

CREATE TABLE IF NOT EXISTS graph_layouts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    scope      TEXT NOT NULL DEFAULT 'global',
    node_kind  TEXT NOT NULL,
    node_id    TEXT NOT NULL,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(scope, node_kind, node_id)
);
