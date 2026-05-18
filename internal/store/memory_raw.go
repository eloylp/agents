package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ReadMemory returns the stored memory string, a found flag, and the
// last-updated timestamp for (workspace, agent, repo). If no row exists, it returns
// ("", false, time.Time{}, nil). An empty content string with found=true
// means the agent intentionally cleared its memory.
func ReadMemory(db *sql.DB, workspace, agent, repo string) (string, bool, time.Time, error) {
	var content, updatedAt string
	err := db.QueryRow(
		"SELECT content, updated_at FROM memory WHERE workspace_id=? AND agent=? AND repo=?", workspace, agent, repo,
	).Scan(&content, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, time.Time{}, nil
	}
	if err != nil {
		return "", false, time.Time{}, fmt.Errorf("store: read memory %s/%s/%s: %w", workspace, agent, repo, err)
	}
	// The modernc.org/sqlite driver returns TIMESTAMP columns as RFC3339 strings
	// (e.g. "2026-04-21T10:30:00Z"). Parse with time.RFC3339 and fall back to
	// the bare "YYYY-MM-DD HH:MM:SS" SQLite text format as a safety net.
	t, parseErr := time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		t, _ = time.Parse(time.DateTime, updatedAt)
	}
	return content, true, t.UTC(), nil
}

// WriteMemory upserts the memory string for (workspace, agent, repo), setting updated_at
// to the current UTC timestamp.
func WriteMemory(db *sql.DB, workspace, agent, repo, content string) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO memory(workspace_id,agent,repo,content,updated_at) VALUES(?,?,?,?,datetime('now'))`,
		workspace, agent, repo, content,
	)
	if err != nil {
		return fmt.Errorf("store: write memory %s/%s/%s: %w", workspace, agent, repo, err)
	}
	return nil
}
