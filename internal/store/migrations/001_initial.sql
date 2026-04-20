-- backends holds the AI backend CLI configurations.
CREATE TABLE IF NOT EXISTS backends (
    name               TEXT    NOT NULL PRIMARY KEY,
    command            TEXT    NOT NULL,
    args               TEXT    NOT NULL DEFAULT '[]',  -- JSON array of strings
    env                TEXT    NOT NULL DEFAULT '{}',  -- JSON object: key→value
    timeout_seconds    INTEGER NOT NULL DEFAULT 600,
    max_prompt_chars   INTEGER NOT NULL DEFAULT 12000,
    redaction_salt_env TEXT    NOT NULL DEFAULT ''
);

-- skills holds reusable guidance blocks.
CREATE TABLE IF NOT EXISTS skills (
    name   TEXT NOT NULL PRIMARY KEY,
    prompt TEXT NOT NULL
);

-- agents holds named agent definitions.
CREATE TABLE IF NOT EXISTS agents (
    name           TEXT    NOT NULL PRIMARY KEY,
    backend        TEXT    NOT NULL DEFAULT 'auto',
    skills         TEXT    NOT NULL DEFAULT '[]',  -- JSON array of skill names
    prompt         TEXT    NOT NULL,
    allow_prs      INTEGER NOT NULL DEFAULT 0,
    allow_dispatch INTEGER NOT NULL DEFAULT 0,
    can_dispatch   TEXT    NOT NULL DEFAULT '[]',  -- JSON array of agent names
    description    TEXT    NOT NULL DEFAULT ''
);

-- repos holds the set of monitored GitHub repositories.
CREATE TABLE IF NOT EXISTS repos (
    name    TEXT    NOT NULL PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 1
);

-- bindings wires agents to triggers on a repository.
CREATE TABLE IF NOT EXISTS bindings (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    repo    TEXT    NOT NULL REFERENCES repos(name),
    agent   TEXT    NOT NULL REFERENCES agents(name),
    labels  TEXT    NOT NULL DEFAULT '[]',  -- JSON array of label strings
    events  TEXT    NOT NULL DEFAULT '[]',  -- JSON array of event kind strings
    cron    TEXT    NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1
);

-- config stores infrastructure key-value pairs (log, http, processor, proxy, memory_dir).
CREATE TABLE IF NOT EXISTS config (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
