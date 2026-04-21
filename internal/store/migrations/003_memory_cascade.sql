-- Add foreign key on memory.agent → agents.name with ON DELETE CASCADE.
-- SQLite does not support ALTER TABLE ADD FOREIGN KEY, so we recreate.

CREATE TABLE memory_new (
    agent      TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    repo       TEXT NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent, repo)
);

INSERT INTO memory_new (agent, repo, content, updated_at)
    SELECT agent, repo, content, updated_at FROM memory
    WHERE agent IN (SELECT name FROM agents);

DROP TABLE memory;
ALTER TABLE memory_new RENAME TO memory;
