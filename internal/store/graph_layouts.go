package store

import (
	"database/sql"
	"fmt"
)

// GraphNodePosition is the persisted manual position for a graph node.
type GraphNodePosition struct {
	NodeID string  `json:"node_id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// ReadGraphLayout returns the global agent-node layout keyed by stable agent ID.
func ReadGraphLayout(db *sql.DB) ([]GraphNodePosition, error) {
	rows, err := db.Query(`
		SELECT node_id, x, y
		FROM graph_layouts
		WHERE scope = 'global' AND node_kind = 'agent'
		ORDER BY node_id`)
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

// UpsertGraphLayout saves global agent-node positions by stable agent ID.
func UpsertGraphLayout(db *sql.DB, positions []GraphNodePosition) error {
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
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)", p.NodeID).Scan(&exists); err != nil {
			return fmt.Errorf("store: upsert graph layout: check agent %s: %w", p.NodeID, err)
		}
		if !exists {
			return &ErrValidation{Msg: fmt.Sprintf("store: upsert graph layout: unknown agent id %q", p.NodeID)}
		}
		if _, err := tx.Exec(`
			INSERT INTO graph_layouts(scope, node_kind, node_id, x, y, updated_at)
			VALUES ('global', 'agent', ?, ?, ?, datetime('now'))
			ON CONFLICT(scope, node_kind, node_id) DO UPDATE SET
				x = excluded.x,
				y = excluded.y,
				updated_at = datetime('now')`,
			p.NodeID, p.X, p.Y,
		); err != nil {
			return fmt.Errorf("store: upsert graph layout: save %s: %w", p.NodeID, err)
		}
	}

	return tx.Commit()
}

// ClearGraphLayout removes the global agent-node layout.
func ClearGraphLayout(db *sql.DB) error {
	if _, err := db.Exec("DELETE FROM graph_layouts WHERE scope = 'global' AND node_kind = 'agent'"); err != nil {
		return fmt.Errorf("store: clear graph layout: %w", err)
	}
	return nil
}
