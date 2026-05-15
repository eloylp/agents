-- Add workspace dimensions to observability/event rows and token budgets.
-- Existing rows are assigned to the Default workspace for compatibility.

ALTER TABLE traces RENAME TO traces_legacy_workspace_024;

CREATE TABLE traces (
    span_id          TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
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
    prompt_gz        BLOB NULL,
    prompt_size      INTEGER NULL,
    input_tokens     INTEGER NULL,
    output_tokens    INTEGER NULL,
    cache_read_tokens INTEGER NULL,
    cache_write_tokens INTEGER NULL,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO traces (
    span_id, workspace_id, root_event_id, parent_span_id, agent, backend, repo,
    number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count,
    summary, started_at, finished_at, duration_ms, status, error, prompt_gz,
    prompt_size, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at
)
SELECT
    span_id, 'default', root_event_id, parent_span_id, agent, backend, repo,
    number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count,
    summary, started_at, finished_at, duration_ms, status, error, prompt_gz,
    prompt_size, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at
FROM traces_legacy_workspace_024;

DROP TABLE traces_legacy_workspace_024;

CREATE INDEX IF NOT EXISTS idx_traces_root ON traces(root_event_id);
CREATE INDEX IF NOT EXISTS idx_traces_agent ON traces(workspace_id, agent);
CREATE INDEX IF NOT EXISTS idx_traces_started ON traces(workspace_id, started_at);
CREATE INDEX IF NOT EXISTS idx_traces_workspace_repo ON traces(workspace_id, repo);

ALTER TABLE events RENAME TO events_legacy_workspace_024;

CREATE TABLE events (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    at           TIMESTAMP NOT NULL,
    repo         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    number       INTEGER NOT NULL DEFAULT 0,
    actor        TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO events (id, workspace_id, at, repo, kind, number, actor, payload)
SELECT id, 'default', at, repo, kind, number, actor, payload
FROM events_legacy_workspace_024;

DROP TABLE events_legacy_workspace_024;

CREATE INDEX IF NOT EXISTS idx_events_at ON events(workspace_id, at);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(workspace_id, kind);

ALTER TABLE event_queue RENAME TO event_queue_legacy_workspace_024;

CREATE TABLE event_queue (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    event_blob   TEXT NOT NULL,
    enqueued_at  TEXT NOT NULL,
    started_at   TEXT NULL,
    completed_at TEXT NULL
);

INSERT INTO event_queue (id, workspace_id, event_blob, enqueued_at, started_at, completed_at)
SELECT id, 'default', event_blob, enqueued_at, started_at, completed_at
FROM event_queue_legacy_workspace_024;

DROP TABLE event_queue_legacy_workspace_024;

CREATE INDEX idx_event_queue_pending
    ON event_queue(id)
    WHERE completed_at IS NULL;
CREATE INDEX idx_event_queue_workspace
    ON event_queue(workspace_id, id);

ALTER TABLE dispatch_history RENAME TO dispatch_history_legacy_workspace_024;

CREATE TABLE dispatch_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL DEFAULT 'default' REFERENCES workspaces(id) ON DELETE CASCADE,
    from_agent   TEXT NOT NULL,
    to_agent     TEXT NOT NULL,
    repo         TEXT NOT NULL,
    number       INTEGER NOT NULL DEFAULT 0,
    reason       TEXT NOT NULL DEFAULT '',
    at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO dispatch_history (id, workspace_id, from_agent, to_agent, repo, number, reason, at)
SELECT id, 'default', from_agent, to_agent, repo, number, reason, at
FROM dispatch_history_legacy_workspace_024;

DROP TABLE dispatch_history_legacy_workspace_024;

CREATE INDEX IF NOT EXISTS idx_dispatch_from ON dispatch_history(workspace_id, from_agent);
CREATE INDEX IF NOT EXISTS idx_dispatch_at ON dispatch_history(workspace_id, at);

ALTER TABLE token_budgets RENAME TO token_budgets_legacy_workspace_024;

CREATE TABLE token_budgets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_kind   TEXT    NOT NULL DEFAULT 'global',
    scope_name   TEXT    NOT NULL DEFAULT '',
    workspace_id TEXT    NOT NULL DEFAULT '',
    repo         TEXT    NOT NULL DEFAULT '',
    agent        TEXT    NOT NULL DEFAULT '',
    backend      TEXT    NOT NULL DEFAULT '',
    period       TEXT    NOT NULL DEFAULT 'daily',
    cap_tokens   INTEGER NOT NULL DEFAULT 0,
    alert_at_pct INTEGER NOT NULL DEFAULT 80,
    enabled      INTEGER NOT NULL DEFAULT 1,
    UNIQUE(scope_kind, workspace_id, repo, agent, backend, period)
);

INSERT INTO token_budgets (
    id, scope_kind, scope_name, workspace_id, repo, agent, backend,
    period, cap_tokens, alert_at_pct, enabled
)
SELECT
    id,
    scope_kind,
    scope_name,
    CASE WHEN scope_kind = 'workspace' THEN scope_name ELSE '' END,
    CASE WHEN scope_kind = 'repo' THEN scope_name ELSE '' END,
    CASE WHEN scope_kind = 'agent' THEN scope_name ELSE '' END,
    CASE WHEN scope_kind = 'backend' THEN scope_name ELSE '' END,
    period,
    cap_tokens,
    alert_at_pct,
    enabled
FROM token_budgets_legacy_workspace_024;

DROP TABLE token_budgets_legacy_workspace_024;
