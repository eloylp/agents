-- Add backend discovery metadata and per-agent model selection support.

ALTER TABLE backends ADD COLUMN version TEXT NOT NULL DEFAULT '';
ALTER TABLE backends ADD COLUMN models TEXT NOT NULL DEFAULT '[]';
ALTER TABLE backends ADD COLUMN healthy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backends ADD COLUMN health_detail TEXT NOT NULL DEFAULT '';
ALTER TABLE backends ADD COLUMN local_model_url TEXT NOT NULL DEFAULT '';

ALTER TABLE agents ADD COLUMN model TEXT NOT NULL DEFAULT '';
