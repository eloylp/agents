-- Add per-agent memory toggle. Defaults to 1 (true) so existing autonomous
-- agents preserve their current behaviour after the migration runs; new
-- callers can opt out by passing 0.
ALTER TABLE agents ADD COLUMN allow_memory INTEGER NOT NULL DEFAULT 1;
