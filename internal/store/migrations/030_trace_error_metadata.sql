CREATE TABLE IF NOT EXISTS traces (
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

ALTER TABLE traces ADD COLUMN error_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN error_detail TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_traces_root ON traces(root_event_id);
CREATE INDEX IF NOT EXISTS idx_traces_agent ON traces(workspace_id, agent);
CREATE INDEX IF NOT EXISTS idx_traces_started ON traces(workspace_id, started_at);
CREATE INDEX IF NOT EXISTS idx_traces_workspace_repo ON traces(workspace_id, repo);
