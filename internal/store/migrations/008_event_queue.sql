-- 008_event_queue: durable queue for in-flight workflow events.
--
-- Every event the daemon enqueues (webhook deliveries, /run requests, cron
-- ticks, inter-agent dispatches) is persisted here before the in-memory
-- channel notifies workers. On clean shutdown the table is mostly empty;
-- on a crash, rows whose completed_at is NULL are replayed at next startup
-- so no work is lost. In-flight runs that were killed mid-prompt
-- re-execute on replay — agents are expected to be idempotent enough that
-- a second run on the same input is acceptable.

CREATE TABLE event_queue (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    event_blob   TEXT NOT NULL,
    enqueued_at  TEXT NOT NULL,
    started_at   TEXT NULL,
    completed_at TEXT NULL
);

-- Index used by the startup replay scan.
CREATE INDEX idx_event_queue_pending
    ON event_queue(id)
    WHERE completed_at IS NULL;
