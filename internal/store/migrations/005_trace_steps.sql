-- Phase 5: Per-span tool-loop transcript (Level 2 observability).
-- Stores the structured sequence of tool calls executed during an agent run.
-- Bounded to 100 steps per span; input/output truncated to 200 chars each.
--
-- No foreign key to traces(span_id) is declared here: both RecordSpan and
-- RecordSteps run as concurrent goroutines after a run completes, and a
-- REFERENCES constraint would cause an integrity error if steps are written
-- before the parent span row is committed. span_id is indexed for fast lookup.
CREATE TABLE IF NOT EXISTS trace_steps (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    span_id        TEXT    NOT NULL,
    step_index     INTEGER NOT NULL,
    tool_name      TEXT    NOT NULL,
    input_summary  TEXT    NOT NULL DEFAULT '',
    output_summary TEXT    NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_trace_steps_span ON trace_steps(span_id, step_index);
