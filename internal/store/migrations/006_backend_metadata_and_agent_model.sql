-- Add backend discovery metadata, per-agent model selection, and drop
-- legacy args/env columns that are now hardcoded in the runner.

CREATE TABLE backends_new (
    name             TEXT PRIMARY KEY,
    command          TEXT NOT NULL,
    version          TEXT NOT NULL DEFAULT '',
    models           TEXT NOT NULL DEFAULT '[]',
    healthy          INTEGER NOT NULL DEFAULT 0,
    health_detail    TEXT NOT NULL DEFAULT '',
    local_model_url  TEXT NOT NULL DEFAULT '',
    timeout_seconds  INTEGER NOT NULL DEFAULT 600,
    max_prompt_chars INTEGER NOT NULL DEFAULT 12000,
    redaction_salt_env TEXT NOT NULL DEFAULT ''
);

INSERT INTO backends_new (name, command, timeout_seconds, max_prompt_chars, redaction_salt_env)
    SELECT name, command, timeout_seconds, max_prompt_chars, redaction_salt_env FROM backends;

DROP TABLE backends;
ALTER TABLE backends_new RENAME TO backends;

ALTER TABLE agents ADD COLUMN model TEXT NOT NULL DEFAULT '';
