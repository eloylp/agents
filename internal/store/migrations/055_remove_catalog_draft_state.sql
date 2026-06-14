-- 055_remove_catalog_draft_state: catalog edits publish immediately.
-- Proposal bundles remain inert in self_improvement_* tables until finalized;
-- catalog *_versions tables only store immutable published history.

DELETE FROM prompt_versions WHERE state <> 'published';
DELETE FROM skill_versions WHERE state <> 'published';
DELETE FROM guardrail_versions WHERE state <> 'published';

CREATE TABLE prompt_versions_055 (
    id              TEXT PRIMARY KEY,
    prompt_id       TEXT NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL CHECK (version_number > 0),
    state           TEXT NOT NULL DEFAULT 'published' CHECK (state = 'published'),
    description     TEXT NOT NULL DEFAULT '',
    content         TEXT NOT NULL,
    source_type     TEXT NOT NULL DEFAULT 'migration',
    source_ref      TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES prompt_versions_055(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(prompt_id, version_number)
);

INSERT INTO prompt_versions_055 (
    id, prompt_id, version_number, state, description, content, source_type,
    source_ref, author, changelog, base_version_id, body_hash, created_at,
    published_at
)
SELECT
    v.id, v.prompt_id, v.version_number, 'published', v.description, v.content,
    v.source_type, v.source_ref, v.author, v.changelog,
    CASE
        WHEN v.base_version_id IS NOT NULL
         AND EXISTS (SELECT 1 FROM prompt_versions b WHERE b.id = v.base_version_id AND b.state = 'published')
        THEN v.base_version_id
        ELSE NULL
    END,
    v.body_hash, v.created_at, COALESCE(v.published_at, v.created_at)
FROM prompt_versions v
WHERE v.state = 'published'
ORDER BY v.version_number;

DROP TABLE prompt_versions;
ALTER TABLE prompt_versions_055 RENAME TO prompt_versions;

CREATE TABLE skill_versions_055 (
    id              TEXT PRIMARY KEY,
    skill_id        TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL CHECK (version_number > 0),
    state           TEXT NOT NULL DEFAULT 'published' CHECK (state = 'published'),
    prompt          TEXT NOT NULL,
    source_type     TEXT NOT NULL DEFAULT 'migration',
    source_ref      TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES skill_versions_055(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(skill_id, version_number)
);

INSERT INTO skill_versions_055 (
    id, skill_id, version_number, state, prompt, source_type, source_ref,
    author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    v.id, v.skill_id, v.version_number, 'published', v.prompt, v.source_type,
    v.source_ref, v.author, v.changelog,
    CASE
        WHEN v.base_version_id IS NOT NULL
         AND EXISTS (SELECT 1 FROM skill_versions b WHERE b.id = v.base_version_id AND b.state = 'published')
        THEN v.base_version_id
        ELSE NULL
    END,
    v.body_hash, v.created_at, COALESCE(v.published_at, v.created_at)
FROM skill_versions v
WHERE v.state = 'published'
ORDER BY v.version_number;

DROP TABLE skill_versions;
ALTER TABLE skill_versions_055 RENAME TO skill_versions;

CREATE TABLE guardrail_versions_055 (
    id              TEXT PRIMARY KEY,
    guardrail_id    TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL CHECK (version_number > 0),
    state           TEXT NOT NULL DEFAULT 'published' CHECK (state = 'published'),
    description     TEXT NOT NULL DEFAULT '',
    content         TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    position        INTEGER NOT NULL DEFAULT 100,
    source_type     TEXT NOT NULL DEFAULT 'migration',
    source_ref      TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES guardrail_versions_055(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(guardrail_id, version_number)
);

INSERT INTO guardrail_versions_055 (
    id, guardrail_id, version_number, state, description, content, enabled,
    position, source_type, source_ref, author, changelog, base_version_id,
    body_hash, created_at, published_at
)
SELECT
    v.id, v.guardrail_id, v.version_number, 'published', v.description,
    v.content, v.enabled, v.position, v.source_type, v.source_ref, v.author,
    v.changelog,
    CASE
        WHEN v.base_version_id IS NOT NULL
         AND EXISTS (SELECT 1 FROM guardrail_versions b WHERE b.id = v.base_version_id AND b.state = 'published')
        THEN v.base_version_id
        ELSE NULL
    END,
    v.body_hash, v.created_at, COALESCE(v.published_at, v.created_at)
FROM guardrail_versions v
WHERE v.state = 'published'
ORDER BY v.version_number;

DROP TABLE guardrail_versions;
ALTER TABLE guardrail_versions_055 RENAME TO guardrail_versions;
