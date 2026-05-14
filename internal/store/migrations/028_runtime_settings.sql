-- Runtime settings for ephemeral agent runner containers.
CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT OR IGNORE INTO config (key, value)
VALUES ('runtime', '{"runner_image":"ghcr.io/eloylp/agents-runner:latest","constraints":{}}');

ALTER TABLE workspaces ADD COLUMN runner_image TEXT NOT NULL DEFAULT '';
