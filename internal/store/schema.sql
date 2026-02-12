CREATE TABLE IF NOT EXISTS repos (
    id bigserial PRIMARY KEY,
    full_name text UNIQUE NOT NULL,
    enabled bool NOT NULL DEFAULT true,
    poll_interval_seconds int NOT NULL DEFAULT 60,
    last_issue_updated_at timestamptz NULL,
    last_pr_updated_at timestamptz NULL,
    created_at timestamptz DEFAULT now(),
    updated_at timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS work_items (
    id bigserial PRIMARY KEY,
    repo_full_name text NOT NULL,
    kind text NOT NULL,
    number int NOT NULL,
    last_seen_updated_at timestamptz NULL,
    last_seen_head_sha text NULL,
    created_at timestamptz DEFAULT now(),
    updated_at timestamptz DEFAULT now(),
    UNIQUE (repo_full_name, kind, number)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id bigserial PRIMARY KEY,
    work_item_id bigint REFERENCES work_items(id),
    workflow text NOT NULL,
    fingerprint text NOT NULL,
    status text NOT NULL,
    error text NULL,
    started_at timestamptz DEFAULT now(),
    finished_at timestamptz NULL,
    UNIQUE (work_item_id, workflow, fingerprint)
);

CREATE TABLE IF NOT EXISTS posted_artifacts (
    id bigserial PRIMARY KEY,
    workflow_run_id bigint REFERENCES workflow_runs(id),
    artifact_type text NOT NULL,
    part_key text NOT NULL,
    github_id text NOT NULL,
    url text NULL,
    created_at timestamptz DEFAULT now(),
    UNIQUE (workflow_run_id, artifact_type, part_key)
);

CREATE TABLE IF NOT EXISTS locks (
    work_item_id bigint PRIMARY KEY REFERENCES work_items(id),
    locked_until timestamptz NOT NULL,
    owner text NOT NULL,
    updated_at timestamptz DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_work_items_repo_kind_number ON work_items(repo_full_name, kind, number);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_item_workflow ON workflow_runs(work_item_id, workflow);
CREATE INDEX IF NOT EXISTS idx_posted_artifacts_run ON posted_artifacts(workflow_run_id);
