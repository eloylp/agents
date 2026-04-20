-- Phase 2: Observability persistence + agent memory.
-- These tables move in-memory ring buffers and filesystem memory to SQLite
-- so data survives daemon restarts and is queryable from the UI.

-- Agent memory (replaces filesystem memory_dir).
-- Full overwrite per run via response.memory field.
CREATE TABLE IF NOT EXISTS memory (
    agent      TEXT NOT NULL,
    repo       TEXT NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent, repo)
);

-- Agent run traces (replaces in-memory TraceBuffer).
CREATE TABLE IF NOT EXISTS traces (
    span_id          TEXT PRIMARY KEY,
    root_event_id    TEXT NOT NULL,
    parent_span_id   TEXT NOT NULL DEFAULT '',
    agent            TEXT NOT NULL,
    backend          TEXT NOT NULL,
    repo             TEXT NOT NULL,
    number           INTEGER NOT NULL DEFAULT 0,
    event_kind       TEXT NOT NULL,
    invoked_by       TEXT NOT NULL DEFAULT '',
    dispatch_depth   INTEGER NOT NULL DEFAULT 0,
    queue_wait_ms    INTEGER NOT NULL DEFAULT 0,
    artifacts_count  INTEGER NOT NULL DEFAULT 0,
    summary          TEXT NOT NULL DEFAULT '',
    started_at       TIMESTAMP NOT NULL,
    finished_at      TIMESTAMP NOT NULL,
    duration_ms      INTEGER NOT NULL,
    status           TEXT NOT NULL,
    error            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_traces_root ON traces(root_event_id);
CREATE INDEX IF NOT EXISTS idx_traces_agent ON traces(agent);
CREATE INDEX IF NOT EXISTS idx_traces_started ON traces(started_at);

-- Webhook and internal events (replaces in-memory EventBuffer).
CREATE TABLE IF NOT EXISTS events (
    id       TEXT PRIMARY KEY,
    at       TIMESTAMP NOT NULL,
    repo     TEXT NOT NULL,
    kind     TEXT NOT NULL,
    number   INTEGER NOT NULL DEFAULT 0,
    actor    TEXT NOT NULL DEFAULT '',
    payload  TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);

-- Dispatch history (replaces in-memory InteractionGraph).
CREATE TABLE IF NOT EXISTS dispatch_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    from_agent TEXT NOT NULL,
    to_agent   TEXT NOT NULL,
    repo       TEXT NOT NULL,
    number     INTEGER NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT '',
    at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_dispatch_from ON dispatch_history(from_agent);
CREATE INDEX IF NOT EXISTS idx_dispatch_at ON dispatch_history(at);

-- Prompt version history (for UI editor rollback).
CREATE TABLE IF NOT EXISTS prompt_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_type TEXT NOT NULL,   -- 'agent' or 'skill'
    entity_name TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    changed_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    changed_by  TEXT NOT NULL DEFAULT 'system'
);

CREATE INDEX IF NOT EXISTS idx_prompt_versions_entity ON prompt_versions(entity_type, entity_name);

-- Response schema (stored in DB instead of file).
CREATE TABLE IF NOT EXISTS response_schema (
    id     INTEGER PRIMARY KEY,
    schema TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);
