-- Add catalog visibility scope to prompts. Existing prompt rows become global
-- catalog items; later rows may be workspace- or repo-scoped. Repo-scoped
-- prompts always require a workspace because repo names are workspace-local.

CREATE TABLE prompts_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT DEFAULT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    repo         TEXT DEFAULT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (workspace_id IS NOT NULL OR repo IS NULL),
    FOREIGN KEY (workspace_id, repo) REFERENCES repos(workspace_id, name) ON DELETE CASCADE
);

INSERT INTO prompts_new (id, workspace_id, repo, name, description, content, created_at, updated_at)
SELECT id, NULL, NULL, name, description, content, created_at, updated_at
FROM prompts;

DROP TABLE prompts;
ALTER TABLE prompts_new RENAME TO prompts;

CREATE UNIQUE INDEX idx_prompts_global_name
    ON prompts(name)
    WHERE workspace_id IS NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_workspace_name
    ON prompts(workspace_id, name)
    WHERE workspace_id IS NOT NULL AND repo IS NULL;
CREATE UNIQUE INDEX idx_prompts_repo_name
    ON prompts(workspace_id, repo, name)
    WHERE workspace_id IS NOT NULL AND repo IS NOT NULL;
CREATE INDEX idx_prompts_scope ON prompts(workspace_id, repo, name);
