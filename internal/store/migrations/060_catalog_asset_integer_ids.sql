-- Convert catalog asset primary keys from opaque text IDs to SQLite integer
-- primary keys. Public refs stay unchanged and remain the only external
-- catalog identity; immutable version row IDs remain text.

CREATE TABLE prompt_id_map_060 (
    old_id TEXT PRIMARY KEY,
    new_id INTEGER NOT NULL UNIQUE
);

INSERT INTO prompt_id_map_060 (old_id, new_id)
SELECT id, ROW_NUMBER() OVER (ORDER BY id)
FROM prompts;

CREATE TABLE skill_id_map_060 (
    old_id TEXT PRIMARY KEY,
    new_id INTEGER NOT NULL UNIQUE
);

INSERT INTO skill_id_map_060 (old_id, new_id)
SELECT id, ROW_NUMBER() OVER (ORDER BY id)
FROM skills;

CREATE TABLE guardrail_id_map_060 (
    old_id TEXT PRIMARY KEY,
    new_id INTEGER NOT NULL UNIQUE
);

INSERT INTO guardrail_id_map_060 (old_id, new_id)
SELECT id, ROW_NUMBER() OVER (ORDER BY id)
FROM guardrails;

CREATE TABLE agents_copy_060 AS SELECT * FROM agents;
CREATE TABLE bindings_copy_060 AS SELECT * FROM bindings;
CREATE TABLE graph_layouts_copy_060 AS SELECT * FROM graph_layouts;
CREATE TABLE agent_dispatches_copy_060 AS SELECT * FROM agent_dispatches;
CREATE TABLE agent_skills_copy_060 AS SELECT * FROM agent_skills;
CREATE TABLE workspace_guardrails_copy_060 AS SELECT * FROM workspace_guardrails;
CREATE TABLE prompts_copy_060 AS SELECT * FROM prompts;
CREATE TABLE skills_copy_060 AS SELECT * FROM skills;
CREATE TABLE guardrails_copy_060 AS SELECT * FROM guardrails;
CREATE TABLE prompt_versions_copy_060 AS SELECT * FROM prompt_versions;
CREATE TABLE skill_versions_copy_060 AS SELECT * FROM skill_versions;
CREATE TABLE guardrail_versions_copy_060 AS SELECT * FROM guardrail_versions;

DROP TABLE bindings;
DROP TABLE graph_layouts;
DROP TABLE agent_dispatches;
DROP TABLE agent_skills;
DROP TABLE workspace_guardrails;
DROP TABLE agents;
DROP TABLE prompt_versions;
DROP TABLE skill_versions;
DROP TABLE guardrail_versions;
DROP TABLE prompts;
DROP TABLE skills;
DROP TABLE guardrails;

CREATE TABLE prompts (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    ref                TEXT NOT NULL UNIQUE,
    workspace_id       TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    repo               TEXT DEFAULT NULL,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    content            TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
    current_version_id TEXT DEFAULT NULL,
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO prompts (id, ref, workspace_id, repo, name, description, content, created_at, updated_at, current_version_id)
SELECT pm.new_id, p.ref, p.workspace_id, p.repo, p.name, p.description, p.content, p.created_at, p.updated_at, p.current_version_id
FROM prompts_copy_060 p
JOIN prompt_id_map_060 pm ON pm.old_id = p.id;

CREATE UNIQUE INDEX idx_prompts_global_name ON prompts(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_workspace_name ON prompts(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_repo_name ON prompts(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_prompts_scope ON prompts(workspace_id, repo, name);

CREATE TABLE skills (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    ref                TEXT NOT NULL UNIQUE,
    workspace_id       TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    repo               TEXT DEFAULT NULL,
    name               TEXT NOT NULL,
    prompt             TEXT NOT NULL,
    current_version_id TEXT DEFAULT NULL,
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO skills (id, ref, workspace_id, repo, name, prompt, current_version_id)
SELECT sm.new_id, s.ref, s.workspace_id, s.repo, s.name, s.prompt, s.current_version_id
FROM skills_copy_060 s
JOIN skill_id_map_060 sm ON sm.old_id = s.id;

CREATE UNIQUE INDEX idx_skills_global_name ON skills(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_workspace_name ON skills(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_repo_name ON skills(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_skills_scope ON skills(workspace_id, repo, name);

CREATE TABLE guardrails (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    ref                TEXT NOT NULL UNIQUE,
    workspace_id       TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    name               TEXT    NOT NULL,
    description        TEXT,
    content            TEXT    NOT NULL,
    default_content    TEXT,
    is_builtin         INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0, 1)),
    enabled            INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    position           INTEGER NOT NULL DEFAULT 100,
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    current_version_id TEXT DEFAULT NULL
);

INSERT INTO guardrails (
    id, ref, workspace_id, name, description, content, default_content,
    is_builtin, enabled, position, updated_at, current_version_id
)
SELECT gm.new_id, g.ref, g.workspace_id, g.name, g.description, g.content, g.default_content,
       g.is_builtin, g.enabled, g.position, g.updated_at, g.current_version_id
FROM guardrails_copy_060 g
JOIN guardrail_id_map_060 gm ON gm.old_id = g.id;

CREATE UNIQUE INDEX idx_guardrails_global_name ON guardrails(name) WHERE workspace_id IS NULL;
CREATE UNIQUE INDEX idx_guardrails_workspace_name ON guardrails(workspace_id, name) WHERE workspace_id IS NOT NULL;
CREATE INDEX idx_guardrails_scope ON guardrails(workspace_id, name);

CREATE TABLE agents (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE RESTRICT,
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL REFERENCES backends(name) ON DELETE RESTRICT,
    model          TEXT NOT NULL DEFAULT '',
    prompt_id      INTEGER NOT NULL REFERENCES prompts(id) ON DELETE RESTRICT,
    scope_type     TEXT NOT NULL DEFAULT 'workspace' CHECK (scope_type IN ('workspace', 'repo')),
    scope_repo     TEXT NOT NULL DEFAULT '',
    allow_prs      INTEGER NOT NULL DEFAULT 0 CHECK (allow_prs IN (0, 1)),
    allow_dispatch INTEGER NOT NULL DEFAULT 0 CHECK (allow_dispatch IN (0, 1)),
    description    TEXT NOT NULL DEFAULT '',
    allow_memory   INTEGER NOT NULL DEFAULT 1 CHECK (allow_memory IN (0, 1)),
    CHECK ((scope_type = 'workspace' AND scope_repo = '') OR (scope_type = 'repo' AND scope_repo <> '')),
    UNIQUE(workspace_id, name)
);

INSERT INTO agents (
    id, workspace_id, name, backend, model, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch,
    description, allow_memory
)
SELECT a.id, a.workspace_id, a.name, a.backend, a.model, pm.new_id,
       a.scope_type, a.scope_repo, a.allow_prs, a.allow_dispatch,
       a.description, a.allow_memory
FROM agents_copy_060 a
JOIN prompt_id_map_060 pm ON pm.old_id = a.prompt_id;

CREATE UNIQUE INDEX idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX idx_agents_workspace ON agents(workspace_id);
CREATE INDEX idx_agents_prompt ON agents(prompt_id);

CREATE TABLE agent_skills (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_id INTEGER NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY(agent_id, skill_id)
);

INSERT INTO agent_skills (agent_id, skill_id, position)
SELECT ask.agent_id, sm.new_id, ask.position
FROM agent_skills_copy_060 ask
JOIN skill_id_map_060 sm ON sm.old_id = ask.skill_id;

CREATE INDEX idx_agent_skills_skill ON agent_skills(skill_id);

CREATE TABLE agent_dispatches (
    source_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    position        INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    CHECK (source_agent_id <> target_agent_id),
    PRIMARY KEY(source_agent_id, target_agent_id)
);

INSERT INTO agent_dispatches (source_agent_id, target_agent_id, position)
SELECT source_agent_id, target_agent_id, position
FROM agent_dispatches_copy_060;

CREATE INDEX idx_agent_dispatches_target ON agent_dispatches(target_agent_id);

CREATE TABLE bindings (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default',
    repo         TEXT NOT NULL,
    agent        TEXT NOT NULL,
    labels       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(labels)),
    events       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(events)),
    cron         TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, agent) REFERENCES agents(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO bindings (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT id, workspace_id, repo, agent, labels, events, cron, enabled
FROM bindings_copy_060;

CREATE INDEX idx_bindings_workspace_repo ON bindings(workspace_id, repo);
CREATE INDEX idx_bindings_workspace_agent ON bindings(workspace_id, agent);

CREATE TABLE graph_layouts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    node_kind    TEXT NOT NULL CHECK (node_kind = 'agent'),
    node_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    x            REAL NOT NULL,
    y            REAL NOT NULL,
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, node_kind, node_id)
);

INSERT INTO graph_layouts (id, workspace_id, node_kind, node_id, x, y, updated_at)
SELECT id, workspace_id, node_kind, node_id, x, y, updated_at
FROM graph_layouts_copy_060;

CREATE TABLE workspace_guardrails (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    guardrail_name INTEGER NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (workspace_id, guardrail_name)
);

INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
SELECT wg.workspace_id, gm.new_id, wg.position, wg.enabled
FROM workspace_guardrails_copy_060 wg
JOIN guardrail_id_map_060 gm ON gm.old_id = wg.guardrail_name;

CREATE TABLE prompt_versions (
    id              TEXT PRIMARY KEY,
    prompt_id       INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL CHECK (version_number > 0),
    state           TEXT NOT NULL DEFAULT 'published' CHECK (state = 'published'),
    description     TEXT NOT NULL DEFAULT '',
    content         TEXT NOT NULL,
    source_type     TEXT NOT NULL DEFAULT 'migration',
    source_ref      TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES prompt_versions(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(prompt_id, version_number)
);

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content, source_type,
    source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT pv.id, pm.new_id, pv.version_number, pv.state, pv.description, pv.content,
       pv.source_type, pv.source_ref, pv.author, pv.changelog,
       pv.base_version_id, pv.body_hash, pv.created_at, pv.published_at
FROM prompt_versions_copy_060 pv
JOIN prompt_id_map_060 pm ON pm.old_id = pv.prompt_id;

CREATE TABLE skill_versions (
    id              TEXT PRIMARY KEY,
    skill_id        INTEGER NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL CHECK (version_number > 0),
    state           TEXT NOT NULL DEFAULT 'published' CHECK (state = 'published'),
    prompt          TEXT NOT NULL,
    source_type     TEXT NOT NULL DEFAULT 'migration',
    source_ref      TEXT NOT NULL DEFAULT '',
    author          TEXT NOT NULL DEFAULT '',
    changelog       TEXT NOT NULL DEFAULT '',
    base_version_id TEXT DEFAULT NULL REFERENCES skill_versions(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(skill_id, version_number)
);

INSERT INTO skill_versions (
    id, skill_id, version_number, state, prompt, source_type, source_ref,
    author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT sv.id, sm.new_id, sv.version_number, sv.state, sv.prompt, sv.source_type,
       sv.source_ref, sv.author, sv.changelog, sv.base_version_id,
       sv.body_hash, sv.created_at, sv.published_at
FROM skill_versions_copy_060 sv
JOIN skill_id_map_060 sm ON sm.old_id = sv.skill_id;

CREATE TABLE guardrail_versions (
    id              TEXT PRIMARY KEY,
    guardrail_id    INTEGER NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
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
    base_version_id TEXT DEFAULT NULL REFERENCES guardrail_versions(id) ON DELETE SET NULL,
    body_hash       TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    published_at    TEXT DEFAULT NULL,
    UNIQUE(guardrail_id, version_number)
);

INSERT INTO guardrail_versions (
    id, guardrail_id, version_number, state, description, content, enabled,
    position, source_type, source_ref, author, changelog, base_version_id,
    body_hash, created_at, published_at
)
SELECT gv.id, gm.new_id, gv.version_number, gv.state, gv.description, gv.content,
       gv.enabled, gv.position, gv.source_type, gv.source_ref, gv.author,
       gv.changelog, gv.base_version_id, gv.body_hash, gv.created_at, gv.published_at
FROM guardrail_versions_copy_060 gv
JOIN guardrail_id_map_060 gm ON gm.old_id = gv.guardrail_id;

CREATE INDEX idx_prompt_versions_prompt ON prompt_versions(prompt_id, version_number);
CREATE INDEX idx_skill_versions_skill ON skill_versions(skill_id, version_number);
CREATE INDEX idx_guardrail_versions_guardrail ON guardrail_versions(guardrail_id, version_number);

CREATE TRIGGER agents_scope_repo_insert
BEFORE INSERT ON agents
WHEN NEW.scope_type = 'repo' AND NOT EXISTS (
    SELECT 1 FROM repos WHERE workspace_id = NEW.workspace_id AND name = NEW.scope_repo
)
BEGIN
    SELECT RAISE(ABORT, 'agent scope_repo references unknown repo');
END;

CREATE TRIGGER agents_scope_repo_update
BEFORE UPDATE OF workspace_id, scope_type, scope_repo ON agents
WHEN NEW.scope_type = 'repo' AND NOT EXISTS (
    SELECT 1 FROM repos WHERE workspace_id = NEW.workspace_id AND name = NEW.scope_repo
)
BEGIN
    SELECT RAISE(ABORT, 'agent scope_repo references unknown repo');
END;

CREATE TRIGGER token_budgets_agent_delete
BEFORE DELETE ON agents
WHEN EXISTS (SELECT 1 FROM token_budgets WHERE workspace_id = OLD.workspace_id AND agent = OLD.name)
BEGIN
    SELECT RAISE(ABORT, 'agent is referenced by token budgets');
END;

DROP TABLE agents_copy_060;
DROP TABLE bindings_copy_060;
DROP TABLE graph_layouts_copy_060;
DROP TABLE agent_dispatches_copy_060;
DROP TABLE agent_skills_copy_060;
DROP TABLE workspace_guardrails_copy_060;
DROP TABLE prompts_copy_060;
DROP TABLE skills_copy_060;
DROP TABLE guardrails_copy_060;
DROP TABLE prompt_versions_copy_060;
DROP TABLE skill_versions_copy_060;
DROP TABLE guardrail_versions_copy_060;
DROP TABLE prompt_id_map_060;
DROP TABLE skill_id_map_060;
DROP TABLE guardrail_id_map_060;
