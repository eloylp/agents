package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

// ReadEnabledGuardrails returns all enabled guardrails in render order
// (position ASC, name ASC). This is the slice the prompt renderer
// prepends to every agent's composed prompt.
func ReadEnabledGuardrails(db *sql.DB) ([]fleet.Guardrail, error) {
	const q = `
		SELECT g.ref, COALESCE(g.workspace_id, ''), g.name, COALESCE(g.description, ''), g.content,
		       COALESCE(g.default_content, ''), g.is_builtin, g.enabled, g.position,
		       COALESCE(gv.id, ''), COALESCE(gv.version_number, 0)
		FROM guardrails g
		LEFT JOIN guardrail_versions gv ON gv.id = g.current_version_id
		WHERE g.enabled = 1
		ORDER BY g.position ASC, g.name ASC`
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
		SELECT g.ref, COALESCE(g.workspace_id, ''), g.name, COALESCE(gv.description, g.description, ''), COALESCE(gv.content, g.content),
		       COALESCE(g.default_content, ''), g.is_builtin, wg.enabled, wg.position,
		       COALESCE(gv.id, ''), COALESCE(gv.version_number, 0)
		FROM workspace_guardrails wg
		JOIN guardrails g ON g.id = wg.guardrail_name
		LEFT JOIN guardrail_versions gv ON gv.id = g.current_version_id
		WHERE wg.workspace_id = ? AND wg.enabled = 1
		ORDER BY wg.position ASC, g.ref ASC`
	return scanGuardrails(db, q, workspaceID)
}

// ReadAllGuardrails returns every guardrail (enabled and disabled),
// ordered the same way the renderer would. Used by the dashboard / API
// surfaces to expose the full list.
func ReadAllGuardrails(db *sql.DB) ([]fleet.Guardrail, error) {
	const q = `
		SELECT g.ref, COALESCE(g.workspace_id, ''), g.name, COALESCE(g.description, ''), g.content,
		       COALESCE(g.default_content, ''), g.is_builtin, g.enabled, g.position,
		       COALESCE(gv.id, ''), COALESCE(gv.version_number, 0)
		FROM guardrails g
		LEFT JOIN guardrail_versions gv ON gv.id = g.current_version_id
		ORDER BY g.position ASC, g.name ASC`
	return scanGuardrails(db, q)
}

// GetGuardrail returns the named guardrail. Returns *ErrNotFound when the
// row does not exist.
func GetGuardrail(db *sql.DB, name string) (fleet.Guardrail, error) {
	return GetGuardrailFrom(db, name)
}

func GetGuardrailFrom(db querier, name string) (fleet.Guardrail, error) {
	name = fleet.NormalizeGuardrailName(name)
	const q = `
		SELECT g.ref, COALESCE(g.workspace_id, ''), g.name, COALESCE(g.description, ''), g.content,
		       COALESCE(g.default_content, ''), g.is_builtin, g.enabled, g.position,
		       COALESCE(gv.id, ''), COALESCE(gv.version_number, 0)
		FROM guardrails g
		LEFT JOIN guardrail_versions gv ON gv.id = g.current_version_id
		WHERE g.id = ? OR g.ref = ? OR (g.workspace_id IS NULL AND g.name = ?)
		ORDER BY CASE WHEN g.id = ? OR g.ref = ? THEN 0 ELSE 1 END
		LIMIT 1`
	g, err := scanGuardrailRow(db.QueryRow(q, name, name, name, name, name))
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Guardrail{}, &ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", name)}
	}
	if err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: get guardrail %q: %w", name, err)
	}
	return g, nil
}

func ReadGuardrailVersion(db *sql.DB, versionID string) (fleet.Guardrail, error) {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return fleet.Guardrail{}, &ErrValidation{Msg: "guardrail version id is required"}
	}
	var g fleet.Guardrail
	var builtin, enabled int
	row := db.QueryRow(`
		SELECT g.ref, COALESCE(g.workspace_id, ''), g.name, gv.description, gv.content,
		       COALESCE(g.default_content, ''), g.is_builtin, gv.enabled, gv.position,
		       gv.id, gv.version_number
		FROM guardrail_versions gv
		JOIN guardrails g ON g.id = gv.guardrail_id
		WHERE gv.id = ? AND gv.state = 'published'`, versionID)
	err := row.Scan(&g.ID, &g.WorkspaceID, &g.Name, &g.Description, &g.Content, &g.DefaultContent,
		&builtin, &enabled, &g.Position, &g.VersionID, &g.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Guardrail{}, &ErrNotFound{Msg: fmt.Sprintf("guardrail version %q not found", versionID)}
	}
	if err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: read guardrail version %s: %w", versionID, err)
	}
	g.IsBuiltin = builtin != 0
	g.Enabled = enabled != 0
	return g, nil
}

// UpsertGuardrail inserts or updates a guardrail. The Name field is
// normalised before persistence. DefaultContent and IsBuiltin are
// preserved across updates: built-in rows keep their canonical default
// even when the operator edits Content. Operator-added rows have empty
// DefaultContent and IsBuiltin = false.
func UpsertGuardrail(db *sql.DB, g fleet.Guardrail) error {
	return UpsertGuardrailTx(db, g)
}

func UpsertGuardrailTx(exec sqlExec, g fleet.Guardrail) error {
	fleet.NormalizeGuardrail(&g)
	if g.Name == "" {
		return &ErrValidation{Msg: "store: guardrail name is required"}
	}
	if g.ID == "" {
		var existingID, existingRef string
		err := queryGuardrailRefByScopeName(exec, g.WorkspaceID, g.Name).Scan(&existingID, &existingRef)
		if err == nil {
			g.ID = existingRef
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: upsert guardrail %q: read existing: %w", g.Name, err)
		}
	}
	if g.ID == "" {
		id, err := derivedCatalogID("guardrail_", g.WorkspaceID, "", g.Name)
		if err != nil {
			return &ErrValidation{Msg: fmt.Sprintf("store: guardrail %q: %v", g.Name, err)}
		}
		g.ID = id
	}
	internalID, _, err := resolveCatalogID(exec, "guardrails", g.ID)
	if errors.Is(err, sql.ErrNoRows) {
		internalID, err = newCatalogInternalID("guardrail_")
	}
	if err != nil {
		return fmt.Errorf("store: upsert guardrail %q: resolve id: %w", g.Name, err)
	}
	if err := validateEntityID(g.ID); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: guardrail %q: %v", g.Name, err)}
	}
	if isReservedGuardrailName(g.Name) {
		return &ErrValidation{Msg: fmt.Sprintf("store: guardrail name %q is reserved for runtime-generated policy", g.Name)}
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
			(id, ref, workspace_id, name, description, content, default_content, is_builtin, enabled, position, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(ref) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			name         = excluded.name,
			description = excluded.description,
			content     = excluded.content,
			enabled     = excluded.enabled,
			position    = excluded.position,
			updated_at  = datetime('now')`
	if _, err := exec.Exec(q,
		internalID, g.ID, g.WorkspaceID, g.Name, g.Description, g.Content, defaultContent, isBuiltin, enabled, g.Position,
	); err != nil {
		if isUniqueConstraint(err) {
			return &ErrConflict{Msg: fmt.Sprintf("guardrail name %q is already used by another guardrail in that scope", g.Name)}
		}
		return fmt.Errorf("store: upsert guardrail %q: %w", g.Name, err)
	}
	version, err := publishGuardrailVersionTx(exec, internalID, g)
	if err != nil {
		return fmt.Errorf("store: upsert guardrail %q: %w", g.Name, err)
	}
	if err := applyGuardrailCurrentVersionTx(exec, internalID, version.ID); err != nil {
		return fmt.Errorf("store: upsert guardrail %q: current version: %w", g.Name, err)
	}
	return nil
}

func isReservedGuardrailName(name string) bool {
	return name == "workspace-boundary"
}

func queryGuardrailRefByScopeName(q querier, workspaceID, name string) *sql.Row {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	}
	if workspaceID == "" {
		return q.QueryRow("SELECT id, ref FROM guardrails WHERE workspace_id IS NULL AND name=?", name)
	}
	return q.QueryRow("SELECT id, ref FROM guardrails WHERE workspace_id=? AND name=?", workspaceID, name)
}

// DeleteGuardrail removes the named guardrail. Built-in rows can be
// deleted too, operators who really mean it can run a complete cleanup;
// the dashboard double-confirms before letting them.
func DeleteGuardrail(db *sql.DB, name string) error {
	return DeleteGuardrailTx(db, name)
}

func DeleteGuardrailTx(db sqlExec, name string) error {
	name = fleet.NormalizeGuardrailName(name)
	id, err := resolveGuardrailInternalID(db, name)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", name)}
	}
	if err != nil {
		return fmt.Errorf("store: delete guardrail %q: lookup: %w", name, err)
	}
	res, err := db.Exec("DELETE FROM guardrails WHERE id = ?", id)
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
	return ResetGuardrailTx(db, name)
}

func ResetGuardrailTx(db sqlExec, name string) error {
	name = fleet.NormalizeGuardrailName(name)
	g, err := GetGuardrailFrom(db, name)
	if err != nil {
		return err
	}
	if g.DefaultContent == "" {
		return &ErrValidation{Msg: fmt.Sprintf("guardrail %q has no default to reset to", name)}
	}
	id, err := resolveGuardrailInternalID(db, name)
	if err != nil {
		return fmt.Errorf("store: reset guardrail %q: lookup: %w", name, err)
	}
	if _, err := db.Exec(
		"UPDATE guardrails SET content = default_content, updated_at = datetime('now') WHERE id = ?",
		id,
	); err != nil {
		return fmt.Errorf("store: reset guardrail %q: %w", name, err)
	}
	g.Content = g.DefaultContent
	version, err := publishGuardrailVersionTx(db, id, g)
	if err != nil {
		return fmt.Errorf("store: reset guardrail %q: %w", name, err)
	}
	if err := applyGuardrailCurrentVersionTx(db, id, version.ID); err != nil {
		return fmt.Errorf("store: reset guardrail %q: current version: %w", name, err)
	}
	return nil
}

func resolveGuardrailInternalID(q querier, ref string) (string, error) {
	ref = fleet.NormalizeGuardrailName(ref)
	var id string
	err := q.QueryRow(`
		SELECT id
		FROM guardrails
		WHERE id = ? OR ref = ? OR (workspace_id IS NULL AND name = ?)
		ORDER BY CASE WHEN id = ? OR ref = ? THEN 0 ELSE 1 END
		LIMIT 1`, ref, ref, ref, ref, ref).Scan(&id)
	return id, err
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
	if err := r.Scan(&g.ID, &g.WorkspaceID, &g.Name, &g.Description, &g.Content, &g.DefaultContent, &isBuiltin, &enabled, &g.Position, &g.VersionID, &g.Version); err != nil {
		return fleet.Guardrail{}, err
	}
	g.IsBuiltin = intToBool(isBuiltin)
	g.Enabled = intToBool(enabled)
	return g, nil
}
