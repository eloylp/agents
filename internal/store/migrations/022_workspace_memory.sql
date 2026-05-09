-- Scope daemon-managed agent memory by workspace. Existing memory rows belong
-- to the migrated Default workspace.

CREATE TABLE IF NOT EXISTS memory (
    agent      TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    repo       TEXT NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent, repo)
);

CREATE TABLE memory_new (
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    agent        TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    repo         TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, agent, repo)
);

INSERT INTO memory_new (workspace_id, agent, repo, content, updated_at)
    SELECT 'default', agent, repo, content, updated_at FROM memory;

DROP TABLE memory;
ALTER TABLE memory_new RENAME TO memory;

CREATE INDEX IF NOT EXISTS idx_memory_workspace ON memory(workspace_id);
