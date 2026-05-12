-- Scope existing agent graph layout rows to the migrated Default workspace.
-- The table already stores a generic scope column from 020_agent_ids_and_graph_layouts.sql.
CREATE TABLE IF NOT EXISTS graph_layouts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    scope      TEXT NOT NULL DEFAULT 'global',
    node_kind  TEXT NOT NULL,
    node_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(scope, node_kind, node_id)
);

UPDATE graph_layouts
SET scope = 'workspace:default'
WHERE scope = 'global';
