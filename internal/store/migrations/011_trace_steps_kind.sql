-- Phase 11: trace_steps records more than tool round-trips. A `kind` column
-- distinguishes the existing tool kind (paired tool_use + tool_result, the
-- default for backward compat) from thinking text blocks emitted by the
-- assistant between tool calls. The Traces detail page renders both
-- through the same card component the Runners live modal uses.
ALTER TABLE trace_steps ADD COLUMN kind TEXT NOT NULL DEFAULT 'tool';
