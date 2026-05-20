-- Decouple catalog FK identity from public, derived refs. The old catalog ids
-- become stable external refs; new primary keys are opaque internal ids used by
-- agents, agent_skills, and workspace_guardrails.

CREATE TABLE prompts_copy_033 AS SELECT * FROM prompts;
CREATE TABLE skills_copy_033 AS SELECT * FROM skills;
CREATE TABLE guardrails_copy_033 AS SELECT * FROM guardrails;

CREATE TABLE prompt_id_map_033 (
    old_id TEXT PRIMARY KEY,
    new_id TEXT NOT NULL UNIQUE
);

INSERT INTO prompt_id_map_033 (old_id, new_id)
SELECT id, 'prompt_' || lower(hex(randomblob(16)))
FROM prompts_copy_033;

CREATE TABLE skill_id_map_033 (
    old_id TEXT PRIMARY KEY,
    new_id TEXT NOT NULL UNIQUE
);

INSERT INTO skill_id_map_033 (old_id, new_id)
SELECT id, 'skill_' || lower(hex(randomblob(16)))
FROM skills_copy_033;

CREATE TABLE guardrail_id_map_033 (
    old_id TEXT PRIMARY KEY,
    new_id TEXT NOT NULL UNIQUE
);

INSERT INTO guardrail_id_map_033 (old_id, new_id)
SELECT id, 'guardrail_' || lower(hex(randomblob(16)))
FROM guardrails_copy_033;

CREATE TABLE agents_copy_033 (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL,
    model          TEXT NOT NULL,
    prompt_id      TEXT NOT NULL,
    scope_type     TEXT NOT NULL,
    scope_repo     TEXT NOT NULL,
    allow_prs      INTEGER NOT NULL,
    allow_dispatch INTEGER NOT NULL,
    description    TEXT NOT NULL,
    allow_memory   INTEGER NOT NULL
);

INSERT INTO agents_copy_033 (
    id, workspace_id, name, backend, model, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch,
    description, allow_memory
)
SELECT a.id, a.workspace_id, a.name, a.backend, a.model, pm.new_id,
       a.scope_type, a.scope_repo, a.allow_prs, a.allow_dispatch,
       a.description, a.allow_memory
FROM agents a
JOIN prompt_id_map_033 pm ON pm.old_id = a.prompt_id;

CREATE TABLE bindings_copy_033 (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repo         TEXT NOT NULL,
    agent        TEXT NOT NULL,
    labels       TEXT NOT NULL,
    events       TEXT NOT NULL,
    cron         TEXT NOT NULL,
    enabled      INTEGER NOT NULL
);

INSERT INTO bindings_copy_033 (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT id, workspace_id, repo, agent, labels, events, cron, enabled
FROM bindings;

CREATE TABLE graph_layouts_copy_033 (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    node_kind    TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    x            REAL NOT NULL,
    y            REAL NOT NULL,
    updated_at   TEXT NOT NULL
);

INSERT INTO graph_layouts_copy_033 (id, workspace_id, node_kind, node_id, x, y, updated_at)
SELECT id, workspace_id, node_kind, node_id, x, y, updated_at
FROM graph_layouts;

CREATE TABLE agent_dispatches_copy_033 (
    source_agent_id TEXT NOT NULL,
    target_agent_id TEXT NOT NULL,
    position        INTEGER NOT NULL
);

INSERT INTO agent_dispatches_copy_033 (source_agent_id, target_agent_id, position)
SELECT source_agent_id, target_agent_id, position
FROM agent_dispatches;

CREATE TABLE agent_skills_copy_033 (
    agent_id TEXT NOT NULL,
    skill_id TEXT NOT NULL,
    position INTEGER NOT NULL
);

INSERT INTO agent_skills_copy_033 (agent_id, skill_id, position)
SELECT ask.agent_id, sm.new_id, ask.position
FROM agent_skills ask
JOIN skill_id_map_033 sm ON sm.old_id = ask.skill_id;

CREATE TABLE workspace_guardrails_copy_033 (
    workspace_id   TEXT NOT NULL,
    guardrail_name TEXT NOT NULL,
    position       INTEGER NOT NULL,
    enabled        INTEGER NOT NULL
);

INSERT INTO workspace_guardrails_copy_033 (workspace_id, guardrail_name, position, enabled)
SELECT wg.workspace_id, gm.new_id, wg.position, wg.enabled
FROM workspace_guardrails wg
JOIN guardrail_id_map_033 gm ON gm.old_id = wg.guardrail_name;

DROP TABLE graph_layouts;
DROP TABLE bindings;
DROP TABLE agent_dispatches;
DROP TABLE agent_skills;
DROP TABLE workspace_guardrails;
DROP TABLE agents;
DROP TABLE prompts;
DROP TABLE skills;
DROP TABLE guardrails;

CREATE TABLE prompts (
    id           TEXT PRIMARY KEY,
    ref          TEXT NOT NULL UNIQUE,
    workspace_id TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    repo         TEXT DEFAULT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO prompts (id, ref, workspace_id, repo, name, description, content, created_at, updated_at)
SELECT pm.new_id, p.id, p.workspace_id, p.repo, p.name, p.description, p.content, p.created_at, p.updated_at
FROM prompts_copy_033 p
JOIN prompt_id_map_033 pm ON pm.old_id = p.id;

CREATE UNIQUE INDEX idx_prompts_global_name ON prompts(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_workspace_name ON prompts(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_repo_name ON prompts(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_prompts_scope ON prompts(workspace_id, repo, name);

CREATE TABLE skills (
    id           TEXT PRIMARY KEY,
    ref          TEXT NOT NULL UNIQUE,
    workspace_id TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    repo         TEXT DEFAULT NULL,
    name         TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO skills (id, ref, workspace_id, repo, name, prompt)
SELECT sm.new_id, s.id, s.workspace_id, s.repo, s.name, s.prompt
FROM skills_copy_033 s
JOIN skill_id_map_033 sm ON sm.old_id = s.id;

CREATE UNIQUE INDEX idx_skills_global_name ON skills(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_workspace_name ON skills(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_repo_name ON skills(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_skills_scope ON skills(workspace_id, repo, name);

CREATE TABLE guardrails (
  id              TEXT PRIMARY KEY,
  ref             TEXT NOT NULL UNIQUE,
  workspace_id    TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  repo            TEXT DEFAULT NULL,
  name            TEXT    NOT NULL,
  description     TEXT,
  content         TEXT    NOT NULL,
  default_content TEXT,
  is_builtin      INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0, 1)),
  enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  position        INTEGER NOT NULL DEFAULT 100,
  updated_at      TEXT    NOT NULL DEFAULT (datetime('now')),
  CHECK (workspace_id IS NOT NULL OR repo IS NULL),
  FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO guardrails (
    id, ref, workspace_id, repo, name, description, content, default_content,
    is_builtin, enabled, position, updated_at
)
SELECT gm.new_id, g.id, g.workspace_id, g.repo, g.name, g.description, g.content, g.default_content,
       g.is_builtin, g.enabled, g.position, g.updated_at
FROM guardrails_copy_033 g
JOIN guardrail_id_map_033 gm ON gm.old_id = g.id;

CREATE UNIQUE INDEX idx_guardrails_global_name ON guardrails(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_workspace_name ON guardrails(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_repo_name ON guardrails(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_guardrails_scope ON guardrails(workspace_id, repo, name);

CREATE TABLE agents (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE RESTRICT,
    name           TEXT NOT NULL,
    backend        TEXT NOT NULL REFERENCES backends(name) ON DELETE RESTRICT,
    model          TEXT NOT NULL DEFAULT '',
    prompt_id      TEXT NOT NULL REFERENCES prompts(id) ON DELETE RESTRICT,
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
SELECT id, workspace_id, name, backend, model, prompt_id,
       scope_type, scope_repo, allow_prs, allow_dispatch,
       description, allow_memory
FROM agents_copy_033;

CREATE UNIQUE INDEX idx_agents_workspace_name ON agents(workspace_id, name);
CREATE INDEX idx_agents_workspace ON agents(workspace_id);
CREATE INDEX idx_agents_prompt ON agents(prompt_id);

CREATE TABLE agent_skills (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY(agent_id, skill_id)
);

INSERT INTO agent_skills (agent_id, skill_id, position)
SELECT agent_id, skill_id, position
FROM agent_skills_copy_033;

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
FROM agent_dispatches_copy_033;

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
FROM bindings_copy_033;

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
FROM graph_layouts_copy_033;

CREATE TABLE workspace_guardrails (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    guardrail_name TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (workspace_id, guardrail_name)
);

INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails_copy_033;

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

DROP TABLE prompts_copy_033;
DROP TABLE skills_copy_033;
DROP TABLE guardrails_copy_033;
DROP TABLE prompt_id_map_033;
DROP TABLE skill_id_map_033;
DROP TABLE guardrail_id_map_033;
DROP TABLE agents_copy_033;
DROP TABLE bindings_copy_033;
DROP TABLE graph_layouts_copy_033;
DROP TABLE agent_dispatches_copy_033;
DROP TABLE agent_skills_copy_033;
DROP TABLE workspace_guardrails_copy_033;
