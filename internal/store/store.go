package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

const maxErrorLength = 2000

type Store struct {
	db *sql.DB
}

type RepoRecord struct {
	ID                  int64
	FullName            string
	Enabled             bool
	PollIntervalSeconds int
	LastIssueUpdatedAt  *time.Time
	LastPRUpdatedAt     *time.Time
}

type WorkItem struct {
	ID                int64
	RepoFullName      string
	Kind              string
	Number            int
	LastSeenUpdatedAt *time.Time
	LastSeenHeadSHA   *string
}

type WorkflowRun struct {
	ID          int64
	WorkItemID  int64
	Workflow    string
	Fingerprint string
	Status      string
	Error       *string
	StartedAt   time.Time
}

type Artifact struct {
	WorkflowRunID int64
	ArtifactType  string
	PartKey       string
	GitHubID      string
	URL           *string
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}

func (s *Store) UpsertRepo(ctx context.Context, repo RepoRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO repos (full_name, enabled, poll_interval_seconds)
VALUES ($1, $2, $3)
ON CONFLICT (full_name) DO UPDATE
SET enabled = EXCLUDED.enabled,
poll_interval_seconds = EXCLUDED.poll_interval_seconds,
updated_at = now()
`, repo.FullName, repo.Enabled, repo.PollIntervalSeconds)
	if err != nil {
		return fmt.Errorf("upsert repo: %w", err)
	}
	return nil
}

func (s *Store) ListRepos(ctx context.Context) ([]RepoRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, full_name, enabled, poll_interval_seconds, last_issue_updated_at, last_pr_updated_at
FROM repos
WHERE enabled = true
ORDER BY full_name
`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()

	var records []RepoRecord
	for rows.Next() {
		var record RepoRecord
		if err := rows.Scan(&record.ID, &record.FullName, &record.Enabled, &record.PollIntervalSeconds, &record.LastIssueUpdatedAt, &record.LastPRUpdatedAt); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list repos rows: %w", err)
	}
	return records, nil
}

func (s *Store) UpdateRepoPollState(ctx context.Context, fullName string, lastIssueUpdatedAt, lastPRUpdatedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE repos
SET last_issue_updated_at = COALESCE($2, last_issue_updated_at),
last_pr_updated_at = COALESCE($3, last_pr_updated_at),
updated_at = now()
WHERE full_name = $1
`, fullName, lastIssueUpdatedAt, lastPRUpdatedAt)
	if err != nil {
		return fmt.Errorf("update repo state: %w", err)
	}
	return nil
}

func (s *Store) EnsureWorkItem(ctx context.Context, repo, kind string, number int) (WorkItem, error) {
	var item WorkItem
	row := s.db.QueryRowContext(ctx, `
INSERT INTO work_items (repo_full_name, kind, number)
VALUES ($1, $2, $3)
ON CONFLICT (repo_full_name, kind, number)
DO UPDATE SET updated_at = now()
RETURNING id, repo_full_name, kind, number, last_seen_updated_at, last_seen_head_sha
`, repo, kind, number)
	if err := row.Scan(&item.ID, &item.RepoFullName, &item.Kind, &item.Number, &item.LastSeenUpdatedAt, &item.LastSeenHeadSHA); err != nil {
		return WorkItem{}, fmt.Errorf("ensure work item: %w", err)
	}
	return item, nil
}

func (s *Store) UpdateWorkItemState(ctx context.Context, id int64, updatedAt *time.Time, headSHA *string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE work_items
SET last_seen_updated_at = COALESCE($2, last_seen_updated_at),
last_seen_head_sha = COALESCE($3, last_seen_head_sha),
updated_at = now()
WHERE id = $1
`, id, updatedAt, headSHA)
	if err != nil {
		return fmt.Errorf("update work item state: %w", err)
	}
	return nil
}

func (s *Store) CreateWorkflowRun(ctx context.Context, workItemID int64, workflow, fingerprint string) (WorkflowRun, bool, error) {
	var run WorkflowRun
	row := s.db.QueryRowContext(ctx, `
INSERT INTO workflow_runs (work_item_id, workflow, fingerprint, status)
VALUES ($1, $2, $3, 'running')
ON CONFLICT (work_item_id, workflow, fingerprint)
DO NOTHING
RETURNING id, work_item_id, workflow, fingerprint, status, error, started_at
`, workItemID, workflow, fingerprint)
	switch err := row.Scan(&run.ID, &run.WorkItemID, &run.Workflow, &run.Fingerprint, &run.Status, &run.Error, &run.StartedAt); {
	case err == nil:
		return run, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return WorkflowRun{}, false, nil
	default:
		return WorkflowRun{}, false, fmt.Errorf("create workflow run: %w", err)
	}
}

func (s *Store) UpdateWorkflowRunStatus(ctx context.Context, runID int64, status string, errMsg *string) error {
	var trimmed *string
	if errMsg != nil {
		value := *errMsg
		if len(value) > maxErrorLength {
			value = value[:maxErrorLength]
		}
		trimmed = &value
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE workflow_runs
SET status = $2,
error = $3,
finished_at = now()
WHERE id = $1
`, runID, status, trimmed)
	if err != nil {
		return fmt.Errorf("update workflow run: %w", err)
	}
	return nil
}

func (s *Store) RecordArtifact(ctx context.Context, artifact Artifact) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO posted_artifacts (workflow_run_id, artifact_type, part_key, github_id, url)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workflow_run_id, artifact_type, part_key)
DO NOTHING
`, artifact.WorkflowRunID, artifact.ArtifactType, artifact.PartKey, artifact.GitHubID, artifact.URL)
	if err != nil {
		return false, fmt.Errorf("record artifact: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record artifact rows affected: %w", err)
	}
	return rows > 0, nil
}

func (s *Store) CountWorkflowRunsSince(ctx context.Context, workItemID int64, workflow string, since time.Time) (int, error) {
	var count int
	row := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM workflow_runs
WHERE work_item_id = $1
AND workflow = $2
AND started_at >= $3
`, workItemID, workflow, since)
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count workflow runs: %w", err)
	}
	return count, nil
}

func (s *Store) TryLock(ctx context.Context, workItemID int64, owner string, duration time.Duration) (bool, error) {
	lockedUntil := time.Now().Add(duration)
	row := s.db.QueryRowContext(ctx, `
INSERT INTO locks (work_item_id, locked_until, owner, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (work_item_id) DO UPDATE
SET locked_until = EXCLUDED.locked_until,
    owner = EXCLUDED.owner,
    updated_at = now()
WHERE locks.locked_until < now()
RETURNING work_item_id
`, workItemID, lockedUntil, owner)
	var id int64
	switch err := row.Scan(&id); {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("try lock: %w", err)
	}
}

func (s *Store) Unlock(ctx context.Context, workItemID int64, owner string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM locks WHERE work_item_id = $1 AND owner = $2
`, workItemID, owner)
	if err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	return nil
}

func SanitizeError(err error) *string {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return nil
	}
	return &msg
}
