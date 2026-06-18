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
	var content string
	var updatedAt SQLiteTime
	err := db.QueryRow(
		"SELECT content, updated_at FROM memory WHERE workspace_id=? AND agent=? AND repo=?", workspace, agent, repo,
	).Scan(&content, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, time.Time{}, nil
	}
	if err != nil {
		return "", false, time.Time{}, fmt.Errorf("store: read memory %s/%s/%s: %w", workspace, agent, repo, err)
	}
	if err := updatedAt.Err(); err != nil {
		return "", false, time.Time{}, fmt.Errorf("store: read memory %s/%s/%s updated_at: %w", workspace, agent, repo, err)
	}
	return content, true, updatedAt.OrZero(), nil
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
