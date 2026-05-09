package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/fleet"
)

// ReadWorkspaces returns all workspaces ordered by name.
func ReadWorkspaces(db *sql.DB) ([]fleet.Workspace, error) {
	rows, err := db.Query("SELECT id, name, description FROM workspaces ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: read workspaces: %w", err)
	}
	defer rows.Close()

	var out []fleet.Workspace
	for rows.Next() {
		var w fleet.Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Description); err != nil {
			return nil, fmt.Errorf("store: read workspaces: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ReadPrompts returns all global prompt catalog entries ordered by name.
func ReadPrompts(db *sql.DB) ([]fleet.Prompt, error) {
	rows, err := db.Query("SELECT id, name, description, content FROM prompts ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: read prompts: %w", err)
	}
	defer rows.Close()

	var out []fleet.Prompt
	for rows.Next() {
		var p fleet.Prompt
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Content); err != nil {
			return nil, fmt.Errorf("store: read prompts: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
