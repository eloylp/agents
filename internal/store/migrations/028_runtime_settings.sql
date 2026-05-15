-- Runtime settings for ephemeral agent runner containers.
INSERT OR IGNORE INTO config (key, value)
VALUES ('runtime', '{"runner_image":"ghcr.io/eloylp/agents-runner:latest","constraints":{}}');

ALTER TABLE workspaces ADD COLUMN runner_image TEXT NOT NULL DEFAULT '';
