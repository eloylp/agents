package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/fleet"
)

// GraphNodePosition is the persisted manual position for a graph node.
type GraphNodePosition struct {
	NodeID string  `json:"node_id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// ReadGraphLayout returns the Default workspace's agent-node layout keyed by
// stable agent ID. New callers should prefer ReadWorkspaceGraphLayout.
func ReadGraphLayout(db *sql.DB) ([]GraphNodePosition, error) {
	return ReadWorkspaceGraphLayout(db, fleet.DefaultWorkspaceID)
}

// ReadWorkspaceGraphLayout returns a workspace-scoped agent-node layout keyed
// by stable agent ID.
func ReadWorkspaceGraphLayout(db *sql.DB, workspace string) ([]GraphNodePosition, error) {
	rows, err := db.Query(`
		SELECT gl.node_id, gl.x, gl.y
		FROM graph_layouts gl
		JOIN agents a ON a.id = gl.node_id
		WHERE gl.scope = ? AND gl.node_kind = 'agent'
			AND COALESCE(NULLIF(a.workspace_id, ''), ?) = ?
		ORDER BY gl.node_id`,
		graphLayoutScope(workspace), fleet.DefaultWorkspaceID, fleet.NormalizeWorkspaceID(workspace))
	if err != nil {
		return nil, fmt.Errorf("store: read graph layout: %w", err)
	}
	defer rows.Close()

	var out []GraphNodePosition
	for rows.Next() {
		var p GraphNodePosition
		if err := rows.Scan(&p.NodeID, &p.X, &p.Y); err != nil {
			return nil, fmt.Errorf("store: scan graph layout: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate graph layout: %w", err)
	}
	return out, nil
}

// UpsertGraphLayout saves Default workspace agent-node positions by stable
// agent ID. New callers should prefer UpsertWorkspaceGraphLayout.
func UpsertGraphLayout(db *sql.DB, positions []GraphNodePosition) error {
	return UpsertWorkspaceGraphLayout(db, fleet.DefaultWorkspaceID, positions)
}

// UpsertWorkspaceGraphLayout saves workspace-scoped agent-node positions by
// stable agent ID.
func UpsertWorkspaceGraphLayout(db *sql.DB, workspace string, positions []GraphNodePosition) error {
	workspace = fleet.NormalizeWorkspaceID(workspace)
	scope := graphLayoutScope(workspace)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert graph layout: begin: %w", err)
	}
	defer tx.Rollback()

	for _, p := range positions {
		if p.NodeID == "" {
			return &ErrValidation{Msg: "store: upsert graph layout: node_id is required"}
		}
		var exists bool
		if err := tx.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM agents
				WHERE id = ? AND COALESCE(NULLIF(workspace_id, ''), ?) = ?
			)`, p.NodeID, fleet.DefaultWorkspaceID, workspace).Scan(&exists); err != nil {
			return fmt.Errorf("store: upsert graph layout: check agent %s: %w", p.NodeID, err)
		}
		if !exists {
			return &ErrValidation{Msg: fmt.Sprintf("store: upsert graph layout: unknown agent id %q in workspace %q", p.NodeID, workspace)}
		}
		if _, err := tx.Exec(`
			INSERT INTO graph_layouts(scope, node_kind, node_id, x, y, updated_at)
			VALUES (?, 'agent', ?, ?, ?, datetime('now'))
			ON CONFLICT(scope, node_kind, node_id) DO UPDATE SET
				x = excluded.x,
				y = excluded.y,
				updated_at = datetime('now')`,
			scope, p.NodeID, p.X, p.Y,
		); err != nil {
			return fmt.Errorf("store: upsert graph layout: save %s: %w", p.NodeID, err)
		}
	}

	return tx.Commit()
}

// ClearGraphLayout removes the Default workspace agent-node layout. New
// callers should prefer ClearWorkspaceGraphLayout.
func ClearGraphLayout(db *sql.DB) error {
	return ClearWorkspaceGraphLayout(db, fleet.DefaultWorkspaceID)
}

// ClearWorkspaceGraphLayout removes a workspace-scoped agent-node layout.
func ClearWorkspaceGraphLayout(db *sql.DB, workspace string) error {
	if _, err := db.Exec("DELETE FROM graph_layouts WHERE scope = ? AND node_kind = 'agent'", graphLayoutScope(workspace)); err != nil {
		return fmt.Errorf("store: clear graph layout: %w", err)
	}
	return nil
}

func graphLayoutScope(workspace string) string {
	return "workspace:" + fleet.NormalizeWorkspaceID(workspace)
}
