-- Phase 9: persist the composed prompt and per-run token usage on each
-- trace span. The prompt is gzipped before storage (text compresses
-- ~5-10x) and decompressed at read time; the size column carries the
-- uncompressed byte count so listings can advertise "fetch a 32 KB
-- prompt" without pulling the body. Token columns capture Anthropic's
-- four-field shape (input / output / cache_creation / cache_read) so
-- operators can spot agents that bust the cache and tune accordingly.
--
-- Old rows from before this migration get NULL for every new column;
-- the UI renders that as "not recorded" rather than zero.
ALTER TABLE traces ADD COLUMN prompt_gz          BLOB;
ALTER TABLE traces ADD COLUMN prompt_size        INTEGER;
ALTER TABLE traces ADD COLUMN input_tokens       INTEGER;
ALTER TABLE traces ADD COLUMN output_tokens      INTEGER;
ALTER TABLE traces ADD COLUMN cache_read_tokens  INTEGER;
ALTER TABLE traces ADD COLUMN cache_write_tokens INTEGER;
