-- Add immutable catalog versions for prompts, skills, and guardrails.
-- Existing mutable rows remain the stable asset identity/current snapshot for
-- compatibility; every existing row is seeded as published version 1.

-- A legacy prompt_versions table from early observability migrations stored
-- editor history for agent/skill inline prompt bodies. Inline bodies have since
-- been removed, so replace that obsolete table with catalog asset versions.
DROP TABLE IF EXISTS prompt_versions;
DROP TABLE IF EXISTS skill_versions;
DROP TABLE IF EXISTS guardrail_versions;

CREATE TABLE prompt_versions (
    id             TEXT PRIMARY KEY,
    prompt_id      TEXT NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    state          TEXT NOT NULL DEFAULT 'published' CHECK (state IN ('draft', 'proposal', 'published')),
    description    TEXT NOT NULL DEFAULT '',
    content        TEXT NOT NULL,
    source_type    TEXT NOT NULL DEFAULT 'migration',
    source_ref     TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    changelog      TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES prompt_versions(id) ON DELETE SET NULL,
    body_hash      TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    published_at   TEXT DEFAULT NULL,
    UNIQUE(prompt_id, version_number)
);

CREATE TABLE skill_versions (
    id             TEXT PRIMARY KEY,
    skill_id       TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    state          TEXT NOT NULL DEFAULT 'published' CHECK (state IN ('draft', 'proposal', 'published')),
    prompt         TEXT NOT NULL,
    source_type    TEXT NOT NULL DEFAULT 'migration',
    source_ref     TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    changelog      TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES skill_versions(id) ON DELETE SET NULL,
    body_hash      TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    published_at   TEXT DEFAULT NULL,
    UNIQUE(skill_id, version_number)
);

CREATE TABLE guardrail_versions (
    id             TEXT PRIMARY KEY,
    guardrail_id   TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    state          TEXT NOT NULL DEFAULT 'published' CHECK (state IN ('draft', 'proposal', 'published')),
    description    TEXT NOT NULL DEFAULT '',
    content        TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    position       INTEGER NOT NULL DEFAULT 100,
    source_type    TEXT NOT NULL DEFAULT 'migration',
    source_ref     TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    changelog      TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES guardrail_versions(id) ON DELETE SET NULL,
    body_hash      TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    published_at   TEXT DEFAULT NULL,
    UNIQUE(guardrail_id, version_number)
);

ALTER TABLE prompts ADD COLUMN current_version_id TEXT DEFAULT NULL;
ALTER TABLE skills ADD COLUMN current_version_id TEXT DEFAULT NULL;
ALTER TABLE guardrails ADD COLUMN current_version_id TEXT DEFAULT NULL;

CREATE TABLE prompt_version_map_035 (
    prompt_id TEXT PRIMARY KEY,
    version_id TEXT NOT NULL UNIQUE
);

INSERT INTO prompt_version_map_035 (prompt_id, version_id)
SELECT id, 'promptver_' || lower(hex(randomblob(16)))
FROM prompts;

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, body_hash, created_at, published_at
)
SELECT m.version_id, p.id, 1, 'published', p.description, p.content,
       'migration', 'migration',
       COALESCE(p.created_at, datetime('now')), COALESCE(p.updated_at, datetime('now'))
FROM prompts p
JOIN prompt_version_map_035 m ON m.prompt_id = p.id;

UPDATE prompts
SET current_version_id = (
    SELECT version_id FROM prompt_version_map_035 WHERE prompt_id = prompts.id
);

DROP TABLE prompt_version_map_035;

CREATE TABLE skill_version_map_035 (
    skill_id TEXT PRIMARY KEY,
    version_id TEXT NOT NULL UNIQUE
);

INSERT INTO skill_version_map_035 (skill_id, version_id)
SELECT id, 'skillver_' || lower(hex(randomblob(16)))
FROM skills;

INSERT INTO skill_versions (
    id, skill_id, version_number, state, prompt,
    source_type, body_hash, created_at, published_at
)
SELECT m.version_id, s.id, 1, 'published', s.prompt,
       'migration', 'migration',
       datetime('now'), datetime('now')
FROM skills s
JOIN skill_version_map_035 m ON m.skill_id = s.id;

UPDATE skills
SET current_version_id = (
    SELECT version_id FROM skill_version_map_035 WHERE skill_id = skills.id
);

DROP TABLE skill_version_map_035;

CREATE TABLE guardrail_version_map_035 (
    guardrail_id TEXT PRIMARY KEY,
    version_id TEXT NOT NULL UNIQUE
);

INSERT INTO guardrail_version_map_035 (guardrail_id, version_id)
SELECT id, 'guardrailver_' || lower(hex(randomblob(16)))
FROM guardrails;

INSERT INTO guardrail_versions (
    id, guardrail_id, version_number, state, description, content,
    enabled, position, source_type, body_hash, created_at, published_at
)
SELECT m.version_id, g.id, 1, 'published', COALESCE(g.description, ''), g.content,
       g.enabled, g.position, 'migration', 'migration',
       datetime('now'), COALESCE(g.updated_at, datetime('now'))
FROM guardrails g
JOIN guardrail_version_map_035 m ON m.guardrail_id = g.id;

UPDATE guardrails
SET current_version_id = (
    SELECT version_id FROM guardrail_version_map_035 WHERE guardrail_id = guardrails.id
);

DROP TABLE guardrail_version_map_035;

ALTER TABLE agents ADD COLUMN prompt_version_id TEXT DEFAULT NULL;
ALTER TABLE agent_skills ADD COLUMN skill_version_id TEXT DEFAULT NULL;
ALTER TABLE workspace_guardrails ADD COLUMN guardrail_version_id TEXT DEFAULT NULL;

CREATE TABLE IF NOT EXISTS traces (
    span_id          TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL DEFAULT 'default',
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
    error_detail     TEXT NOT NULL DEFAULT '',
    prompt_gz        BLOB NULL,
    prompt_size      INTEGER NULL,
    input_tokens     INTEGER NULL,
    output_tokens    INTEGER NULL,
    cache_read_tokens INTEGER NULL,
    cache_write_tokens INTEGER NULL,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE traces ADD COLUMN prompt_version_id TEXT DEFAULT '';
ALTER TABLE traces ADD COLUMN skill_version_ids TEXT DEFAULT '';
ALTER TABLE traces ADD COLUMN guardrail_version_ids TEXT DEFAULT '';

CREATE INDEX idx_prompt_versions_prompt ON prompt_versions(prompt_id, version_number);
CREATE INDEX idx_skill_versions_skill ON skill_versions(skill_id, version_number);
CREATE INDEX idx_guardrail_versions_guardrail ON guardrail_versions(guardrail_id, version_number);
