-- Phase 1: SQLite foundation, fleet config and daemon settings.
-- All entities that previously lived in config.yaml now live here.
-- Arrays and maps are stored as JSON text.

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS backends (
    name               TEXT PRIMARY KEY,
    command            TEXT NOT NULL,
    args               TEXT NOT NULL DEFAULT '[]',
    env                TEXT NOT NULL DEFAULT '{}',
    timeout_seconds    INTEGER NOT NULL DEFAULT 600,
    max_prompt_chars   INTEGER NOT NULL DEFAULT 12000,
    redaction_salt_env TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS skills (
    name   TEXT PRIMARY KEY,
    prompt TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    name           TEXT PRIMARY KEY,
    backend        TEXT NOT NULL DEFAULT 'auto',
    skills         TEXT NOT NULL DEFAULT '[]',
    prompt         TEXT NOT NULL,
    allow_prs      INTEGER NOT NULL DEFAULT 0,
    allow_dispatch INTEGER NOT NULL DEFAULT 0,
    can_dispatch   TEXT NOT NULL DEFAULT '[]',
    description    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS repos (
    name    TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 1
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
