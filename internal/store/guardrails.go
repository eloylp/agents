package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/eloylp/agents/internal/fleet"
)

// ReadEnabledGuardrails returns all enabled guardrails in render order
// (position ASC, name ASC). This is the slice the prompt renderer
// prepends to every agent's composed prompt.
func ReadEnabledGuardrails(db *sql.DB) ([]fleet.Guardrail, error) {
	const q = `
		SELECT name, COALESCE(description, ''), content,
		       COALESCE(default_content, ''), is_builtin, enabled, position
		FROM guardrails
		WHERE enabled = 1
		ORDER BY position ASC, name ASC`
	return scanGuardrails(db, q)
}

// ReadWorkspacePromptGuardrails returns the workspace-selected guardrails in
// prompt render order. Workspace references, not the global catalog enabled
// bit, decide whether a static guardrail is applied to a run.
func ReadWorkspacePromptGuardrails(db *sql.DB, workspace string) ([]fleet.Guardrail, error) {
	workspaceID, err := ResolveWorkspaceID(db, workspace)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT g.name, COALESCE(g.description, ''), g.content,
		       COALESCE(g.default_content, ''), g.is_builtin, wg.enabled, wg.position
		FROM workspace_guardrails wg
		JOIN guardrails g ON g.name = wg.guardrail_name
		WHERE wg.workspace_id = ? AND wg.enabled = 1
		ORDER BY wg.position ASC, wg.guardrail_name ASC`
	return scanGuardrails(db, q, workspaceID)
}

// ReadAllGuardrails returns every guardrail (enabled and disabled),
// ordered the same way the renderer would. Used by the dashboard / API
// surfaces to expose the full list.
func ReadAllGuardrails(db *sql.DB) ([]fleet.Guardrail, error) {
	const q = `
		SELECT name, COALESCE(description, ''), content,
		       COALESCE(default_content, ''), is_builtin, enabled, position
		FROM guardrails
		ORDER BY position ASC, name ASC`
	return scanGuardrails(db, q)
}

// GetGuardrail returns the named guardrail. Returns *ErrNotFound when the
// row does not exist.
func GetGuardrail(db *sql.DB, name string) (fleet.Guardrail, error) {
	name = fleet.NormalizeGuardrailName(name)
	const q = `
		SELECT name, COALESCE(description, ''), content,
		       COALESCE(default_content, ''), is_builtin, enabled, position
		FROM guardrails
		WHERE name = ?`
	g, err := scanGuardrailRow(db.QueryRow(q, name))
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Guardrail{}, &ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", name)}
	}
	if err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: get guardrail %q: %w", name, err)
	}
	return g, nil
}

// UpsertGuardrail inserts or updates a guardrail. The Name field is
// normalised before persistence. DefaultContent and IsBuiltin are
// preserved across updates: built-in rows keep their canonical default
// even when the operator edits Content. Operator-added rows have empty
// DefaultContent and IsBuiltin = false.
func UpsertGuardrail(db *sql.DB, g fleet.Guardrail) error {
	fleet.NormalizeGuardrail(&g)
	if g.Name == "" {
		return &ErrValidation{Msg: "store: guardrail name is required"}
	}
	if g.Content == "" {
		return &ErrValidation{Msg: fmt.Sprintf("store: guardrail %q: content is required", g.Name)}
	}
	enabled := boolToInt(g.Enabled)
	isBuiltin := boolToInt(g.IsBuiltin)
	var defaultContent any = g.DefaultContent
	if g.DefaultContent == "" {
		defaultContent = nil
	}
	// Preserve the existing built-in flag and default_content when the row
	// is operator-edited via PATCH semantics (those fields are not in the
	// public surface). Use INSERT ... ON CONFLICT so a non-built-in update
	// can't accidentally promote itself by writing is_builtin = 1 from
	// inbound JSON.
	const q = `
		INSERT INTO guardrails
			(name, description, content, default_content, is_builtin, enabled, position, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			description = excluded.description,
			content     = excluded.content,
			enabled     = excluded.enabled,
			position    = excluded.position,
			updated_at  = datetime('now')`
	if _, err := db.Exec(q,
		g.Name, g.Description, g.Content, defaultContent, isBuiltin, enabled, g.Position,
	); err != nil {
		return fmt.Errorf("store: upsert guardrail %q: %w", g.Name, err)
	}
	return nil
}

// DeleteGuardrail removes the named guardrail. Built-in rows can be
// deleted too, operators who really mean it can run a complete cleanup;
// the dashboard double-confirms before letting them.
func DeleteGuardrail(db *sql.DB, name string) error {
	name = fleet.NormalizeGuardrailName(name)
	res, err := db.Exec("DELETE FROM guardrails WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("store: delete guardrail %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete guardrail %q: rows affected: %w", name, err)
	}
	if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", name)}
	}
	return nil
}

// ResetGuardrail copies a built-in guardrail's default_content back into
// its content. Returns *ErrNotFound when the row is missing and
// *ErrValidation when the row has no default_content (i.e., it's an
// operator-added rule with nothing to reset to).
func ResetGuardrail(db *sql.DB, name string) error {
	name = fleet.NormalizeGuardrailName(name)
	g, err := GetGuardrail(db, name)
	if err != nil {
		return err
	}
	if g.DefaultContent == "" {
		return &ErrValidation{Msg: fmt.Sprintf("guardrail %q has no default to reset to", name)}
	}
	if _, err := db.Exec(
		"UPDATE guardrails SET content = default_content, updated_at = datetime('now') WHERE name = ?",
		name,
	); err != nil {
		return fmt.Errorf("store: reset guardrail %q: %w", name, err)
	}
	return nil
}

func scanGuardrails(db *sql.DB, query string, args ...any) ([]fleet.Guardrail, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read guardrails: %w", err)
	}
	defer rows.Close()
	var out []fleet.Guardrail
	for rows.Next() {
		g, err := scanGuardrailRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: read guardrails: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGuardrailRow(r rowScanner) (fleet.Guardrail, error) {
	var (
		g                  fleet.Guardrail
		isBuiltin, enabled int
	)
	if err := r.Scan(&g.Name, &g.Description, &g.Content, &g.DefaultContent, &isBuiltin, &enabled, &g.Position); err != nil {
		return fleet.Guardrail{}, err
	}
	g.IsBuiltin = intToBool(isBuiltin)
	g.Enabled = intToBool(enabled)
	return g, nil
}
