-- Strengthen mutable configuration integrity. Observability tables intentionally
-- keep snapshot strings so historical traces/events survive entity deletion.

CREATE TABLE bindings_copy_032 (
    id           INTEGER PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repo         TEXT NOT NULL,
    agent        TEXT NOT NULL,
    labels       TEXT NOT NULL,
    events       TEXT NOT NULL,
    cron         TEXT NOT NULL,
    enabled      INTEGER NOT NULL
);

INSERT INTO bindings_copy_032 (id, workspace_id, repo, agent, labels, events, cron, enabled)
SELECT id, workspace_id, repo, agent, labels, events, cron, enabled
FROM bindings;

CREATE TABLE graph_layouts_copy_032 (
    id         INTEGER PRIMARY KEY,
    scope      TEXT NOT NULL,
    node_kind  TEXT NOT NULL,
    node_id    TEXT NOT NULL,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO graph_layouts_copy_032 (id, scope, node_kind, node_id, x, y, updated_at)
SELECT id, scope, node_kind, node_id, x, y, updated_at
FROM graph_layouts;

CREATE TABLE workspace_guardrails_copy_032 (
    workspace_id   TEXT NOT NULL,
    guardrail_name TEXT NOT NULL,
    position       INTEGER NOT NULL,
    enabled        INTEGER NOT NULL
);

INSERT INTO workspace_guardrails_copy_032 (workspace_id, guardrail_name, position, enabled)
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails;

CREATE TABLE agent_skill_refs_032 (
    agent_id TEXT NOT NULL,
    skill_id TEXT NOT NULL,
    position INTEGER NOT NULL
);

INSERT INTO agent_skill_refs_032 (agent_id, skill_id, position)
SELECT a.id, refs.value, refs.key
FROM agents a, json_each(a.skills) refs;

CREATE TABLE agent_dispatch_refs_032 (
    source_agent_id TEXT NOT NULL,
    target_agent_id TEXT NOT NULL,
    position INTEGER NOT NULL
);

INSERT INTO agent_dispatch_refs_032 (source_agent_id, target_agent_id, position)
SELECT source.id,
       (SELECT target.id FROM agents target WHERE target.workspace_id = source.workspace_id AND target.name = refs.value),
       refs.key
FROM agents source, json_each(source.can_dispatch) refs;

DROP TABLE graph_layouts;
DROP TABLE bindings;
DROP TABLE workspace_guardrails;

CREATE TABLE backends_new (
    name             TEXT PRIMARY KEY,
    command          TEXT NOT NULL,
    version          TEXT NOT NULL DEFAULT '',
    models           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
    healthy          INTEGER NOT NULL DEFAULT 0 CHECK (healthy IN (0, 1)),
    health_detail    TEXT NOT NULL DEFAULT '',
    local_model_url  TEXT NOT NULL DEFAULT '',
    timeout_seconds  INTEGER NOT NULL DEFAULT 600 CHECK (timeout_seconds > 0),
    max_prompt_chars INTEGER NOT NULL DEFAULT 12000 CHECK (max_prompt_chars > 0),
    redaction_salt_env TEXT NOT NULL DEFAULT ''
);

INSERT INTO backends_new (
    name, command, version, models, healthy, health_detail,
    local_model_url, timeout_seconds, max_prompt_chars, redaction_salt_env
)
SELECT name, command, version, models, healthy, health_detail,
       local_model_url, timeout_seconds, max_prompt_chars, redaction_salt_env
FROM backends;

DROP TABLE backends;
ALTER TABLE backends_new RENAME TO backends;

CREATE TABLE repos_new (
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE RESTRICT,
    name         TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY(workspace_id, name)
);

INSERT INTO repos_new (workspace_id, name, enabled)
SELECT workspace_id, name, enabled
FROM repos;

DROP TABLE repos;
ALTER TABLE repos_new RENAME TO repos;
CREATE UNIQUE INDEX idx_repos_workspace_name ON repos(workspace_id, name);
CREATE INDEX idx_repos_workspace ON repos(workspace_id);

CREATE TABLE prompts_new (
    id           TEXT PRIMARY KEY,
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

INSERT INTO prompts_new (id, workspace_id, repo, name, description, content, created_at, updated_at)
SELECT id, workspace_id, repo, name, description, content, created_at, updated_at
FROM prompts;

DROP TABLE prompts;
ALTER TABLE prompts_new RENAME TO prompts;
CREATE UNIQUE INDEX idx_prompts_global_name ON prompts(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_workspace_name ON prompts(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_repo_name ON prompts(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_prompts_scope ON prompts(workspace_id, repo, name);

CREATE TABLE skills_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    repo         TEXT DEFAULT NULL,
    name         TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE RESTRICT
);

INSERT INTO skills_new (id, workspace_id, repo, name, prompt)
SELECT id, workspace_id, repo, name, prompt
FROM skills;

DROP TABLE skills;
ALTER TABLE skills_new RENAME TO skills;
CREATE UNIQUE INDEX idx_skills_global_name ON skills(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_workspace_name ON skills(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_skills_repo_name ON skills(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_skills_scope ON skills(workspace_id, repo, name);

CREATE TABLE guardrails_new (
  id              TEXT PRIMARY KEY,
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

INSERT INTO guardrails_new (
    id, workspace_id, repo, name, description, content, default_content,
    is_builtin, enabled, position, updated_at
)
SELECT id, workspace_id, repo, name, description, content, default_content,
       is_builtin, enabled, position, updated_at
FROM guardrails;

DROP TABLE guardrails;
ALTER TABLE guardrails_new RENAME TO guardrails;
CREATE UNIQUE INDEX idx_guardrails_global_name ON guardrails(name) WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_workspace_name ON guardrails(workspace_id, name) WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_guardrails_repo_name ON guardrails(workspace_id, repo, name) WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_guardrails_scope ON guardrails(workspace_id, repo, name);

CREATE TABLE agents_new (
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

INSERT INTO agents_new (
    id, workspace_id, name, backend, model, prompt_id,
    scope_type, scope_repo, allow_prs, allow_dispatch,
    description, allow_memory
)
SELECT id, workspace_id, name, backend, model, prompt_id,
       scope_type, scope_repo, allow_prs, allow_dispatch,
       description, allow_memory
FROM agents;

DROP TABLE agents;
ALTER TABLE agents_new RENAME TO agents;
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
FROM agent_skill_refs_032;

CREATE INDEX idx_agent_skills_skill ON agent_skills(skill_id);

CREATE TABLE agent_dispatches (
    source_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    CHECK (source_agent_id <> target_agent_id),
    PRIMARY KEY(source_agent_id, target_agent_id)
);

INSERT INTO agent_dispatches (source_agent_id, target_agent_id, position)
SELECT source_agent_id, target_agent_id, position
FROM agent_dispatch_refs_032;

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
FROM bindings_copy_032;

CREATE INDEX idx_bindings_workspace_repo ON bindings(workspace_id, repo);
CREATE INDEX idx_bindings_workspace_agent ON bindings(workspace_id, agent);

CREATE TABLE graph_layouts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    node_kind  TEXT NOT NULL CHECK (node_kind = 'agent'),
    node_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    x          REAL NOT NULL,
    y          REAL NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, node_kind, node_id)
);

INSERT INTO graph_layouts (id, workspace_id, node_kind, node_id, x, y, updated_at)
SELECT id,
       CASE
           WHEN scope LIKE 'workspace:%' THEN substr(scope, length('workspace:') + 1)
           ELSE 'default'
       END,
       node_kind, node_id, x, y, updated_at
FROM graph_layouts_copy_032;

CREATE TABLE workspace_guardrails (
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    guardrail_name TEXT NOT NULL REFERENCES guardrails(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    PRIMARY KEY (workspace_id, guardrail_name)
);

INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
SELECT workspace_id, guardrail_name, position, enabled
FROM workspace_guardrails_copy_032;

CREATE TABLE token_budgets_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_kind   TEXT    NOT NULL DEFAULT 'global' CHECK (scope_kind IN ('global', 'workspace', 'repo', 'agent', 'backend', 'workspace+repo', 'workspace+agent', 'workspace+backend', 'workspace+repo+agent')),
    scope_name   TEXT    NOT NULL DEFAULT '',
    workspace_id TEXT    NOT NULL DEFAULT '',
    repo         TEXT    NOT NULL DEFAULT '',
    agent        TEXT    NOT NULL DEFAULT '',
    backend      TEXT    NOT NULL DEFAULT '',
    period       TEXT    NOT NULL DEFAULT 'daily' CHECK (period IN ('daily', 'weekly', 'monthly')),
    cap_tokens   INTEGER NOT NULL DEFAULT 0 CHECK (cap_tokens > 0),
    alert_at_pct INTEGER NOT NULL DEFAULT 80 CHECK (alert_at_pct BETWEEN 0 AND 100),
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    UNIQUE(scope_kind, workspace_id, repo, agent, backend, period)
);

INSERT INTO token_budgets_new (
    id, scope_kind, scope_name, workspace_id, repo, agent, backend,
    period, cap_tokens, alert_at_pct, enabled
)
SELECT id, scope_kind, scope_name, workspace_id, repo, agent, backend,
       period, cap_tokens, alert_at_pct, enabled
FROM token_budgets;

DROP TABLE token_budgets;
ALTER TABLE token_budgets_new RENAME TO token_budgets;

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

CREATE TRIGGER repos_scope_agent_delete
BEFORE DELETE ON repos
WHEN EXISTS (
    SELECT 1 FROM agents WHERE workspace_id = OLD.workspace_id AND scope_type = 'repo' AND scope_repo = OLD.name
)
BEGIN
    SELECT RAISE(ABORT, 'repo is referenced by a repo-scoped agent');
END;

CREATE TRIGGER token_budgets_validate_insert
BEFORE INSERT ON token_budgets
WHEN (NEW.workspace_id <> '' AND NOT EXISTS (SELECT 1 FROM workspaces WHERE id = NEW.workspace_id))
  OR (NEW.workspace_id <> '' AND NEW.repo <> '' AND NOT EXISTS (SELECT 1 FROM repos WHERE workspace_id = NEW.workspace_id AND name = NEW.repo))
  OR (NEW.workspace_id <> '' AND NEW.agent <> '' AND NOT EXISTS (SELECT 1 FROM agents WHERE workspace_id = NEW.workspace_id AND name = NEW.agent))
  OR (NEW.backend <> '' AND NOT EXISTS (SELECT 1 FROM backends WHERE name = NEW.backend))
BEGIN
    SELECT RAISE(ABORT, 'token budget references unknown entity');
END;

CREATE TRIGGER token_budgets_validate_update
BEFORE UPDATE OF workspace_id, repo, agent, backend ON token_budgets
WHEN (NEW.workspace_id <> '' AND NOT EXISTS (SELECT 1 FROM workspaces WHERE id = NEW.workspace_id))
  OR (NEW.workspace_id <> '' AND NEW.repo <> '' AND NOT EXISTS (SELECT 1 FROM repos WHERE workspace_id = NEW.workspace_id AND name = NEW.repo))
  OR (NEW.workspace_id <> '' AND NEW.agent <> '' AND NOT EXISTS (SELECT 1 FROM agents WHERE workspace_id = NEW.workspace_id AND name = NEW.agent))
  OR (NEW.backend <> '' AND NOT EXISTS (SELECT 1 FROM backends WHERE name = NEW.backend))
BEGIN
    SELECT RAISE(ABORT, 'token budget references unknown entity');
END;

CREATE TRIGGER token_budgets_workspace_delete
BEFORE DELETE ON workspaces
WHEN EXISTS (SELECT 1 FROM token_budgets WHERE workspace_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'workspace is referenced by token budgets');
END;

CREATE TRIGGER token_budgets_repo_delete
BEFORE DELETE ON repos
WHEN EXISTS (SELECT 1 FROM token_budgets WHERE workspace_id = OLD.workspace_id AND repo = OLD.name)
BEGIN
    SELECT RAISE(ABORT, 'repo is referenced by token budgets');
END;

CREATE TRIGGER token_budgets_agent_delete
BEFORE DELETE ON agents
WHEN EXISTS (SELECT 1 FROM token_budgets WHERE workspace_id = OLD.workspace_id AND agent = OLD.name)
BEGIN
    SELECT RAISE(ABORT, 'agent is referenced by token budgets');
END;

CREATE TRIGGER token_budgets_backend_delete
BEFORE DELETE ON backends
WHEN EXISTS (SELECT 1 FROM token_budgets WHERE backend = OLD.name)
BEGIN
    SELECT RAISE(ABORT, 'backend is referenced by token budgets');
END;

DROP TABLE agent_skill_refs_032;
DROP TABLE agent_dispatch_refs_032;
DROP TABLE bindings_copy_032;
DROP TABLE graph_layouts_copy_032;
DROP TABLE workspace_guardrails_copy_032;
