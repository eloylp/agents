-- Phase 2: agent memory stored in SQLite.
-- Each (agent, repo) pair has a single text blob that the agent can overwrite
-- in full via the `memory` field of its structured response. The updated_at
-- column records the last write time for UI display and future TTL cleanup.

CREATE TABLE IF NOT EXISTS memory (
    agent      TEXT NOT NULL,
    repo       TEXT NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (agent, repo)
);
