package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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

// ResolveWorkspaceID resolves either a workspace id or display name to the
// stable workspace id used by storage.
func ResolveWorkspaceID(db *sql.DB, workspace string) (string, error) {
	w, err := ReadWorkspace(db, workspace)
	if err != nil {
		return "", err
	}
	return w.ID, nil
}

// ReadWorkspace resolves either a workspace id or display name. IDs are the
// stable URL contract, so an exact id match wins over a display-name collision.
func ReadWorkspace(db *sql.DB, workspace string) (fleet.Workspace, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = fleet.DefaultWorkspaceID
	}
	var w fleet.Workspace
	err := db.QueryRow("SELECT id, name, description FROM workspaces WHERE id=?", workspace).Scan(&w.ID, &w.Name, &w.Description)
	if err == nil {
		return w, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fleet.Workspace{}, fmt.Errorf("store: read workspace %q by id: %w", workspace, err)
	}
	err = db.QueryRow("SELECT id, name, description FROM workspaces WHERE name=?", workspace).Scan(&w.ID, &w.Name, &w.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Workspace{}, &ErrNotFound{Msg: fmt.Sprintf("workspace %q not found", workspace)}
	}
	if err != nil {
		return fleet.Workspace{}, fmt.Errorf("store: read workspace %q by name: %w", workspace, err)
	}
	return w, nil
}

// UpsertWorkspace creates or updates a workspace and seeds built-in guardrail
// references for newly-created rows.
func UpsertWorkspace(db *sql.DB, w fleet.Workspace) (fleet.Workspace, error) {
	w.ID = strings.TrimSpace(w.ID)
	w.Name = strings.TrimSpace(w.Name)
	w.Description = strings.TrimSpace(w.Description)
	if w.ID == "" {
		id, err := derivedEntityID("", w.Name)
		if err != nil {
			return fleet.Workspace{}, &ErrValidation{Msg: fmt.Sprintf("workspace %q: %v", w.Name, err)}
		}
		w.ID = id
	}
	if w.ID == "" || w.Name == "" {
		return fleet.Workspace{}, &ErrValidation{Msg: "workspace id and name are required"}
	}
	if err := validateEntityID(w.ID); err != nil {
		return fleet.Workspace{}, &ErrValidation{Msg: fmt.Sprintf("workspace %q: %v", w.Name, err)}
	}
	tx, err := db.Begin()
	if err != nil {
		return fleet.Workspace{}, fmt.Errorf("store: upsert workspace %s: begin: %w", w.ID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO workspaces (id, name, description, updated_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			updated_at = datetime('now')`,
		w.ID, w.Name, w.Description,
	); err != nil {
		if isUniqueConstraint(err) {
			return fleet.Workspace{}, &ErrConflict{Msg: fmt.Sprintf("workspace name %q is already used by another workspace id", w.Name)}
		}
		return fleet.Workspace{}, fmt.Errorf("store: upsert workspace %s: %w", w.ID, err)
	}
	if err := seedWorkspaceGuardrails(tx, w.ID); err != nil {
		return fleet.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.Workspace{}, fmt.Errorf("store: upsert workspace %s: commit: %w", w.ID, err)
	}
	return w, nil
}

// DeleteWorkspace removes a workspace only when no agents or repos still
// belong to it. The compatibility default workspace is retained.
func DeleteWorkspace(db *sql.DB, workspace string) error {
	id, err := ResolveWorkspaceID(db, workspace)
	if err != nil {
		return err
	}
	if id == fleet.DefaultWorkspaceID {
		return &ErrConflict{Msg: "default workspace cannot be deleted"}
	}
	var agents, repos int
	if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id=?", id).Scan(&agents); err != nil {
		return fmt.Errorf("store: delete workspace %s: count agents: %w", id, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM repos WHERE workspace_id=?", id).Scan(&repos); err != nil {
		return fmt.Errorf("store: delete workspace %s: count repos: %w", id, err)
	}
	if agents > 0 || repos > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("workspace %q is referenced by %d agent(s) and %d repo(s)", id, agents, repos)}
	}
	res, err := db.Exec("DELETE FROM workspaces WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("store: delete workspace %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: delete workspace %s: rows affected: %w", id, err)
	} else if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("workspace %q not found", workspace)}
	}
	return nil
}

// ReadWorkspaceGuardrails returns a workspace's selected global guardrail
// references in render order.
func ReadWorkspaceGuardrails(db *sql.DB, workspace string) ([]fleet.WorkspaceGuardrailRef, error) {
	workspaceID, err := ResolveWorkspaceID(db, workspace)
	if err != nil {
		return nil, err
	}
	return readWorkspaceGuardrails(db, workspaceID)
}

func readWorkspaceGuardrails(db querier, workspaceID string) ([]fleet.WorkspaceGuardrailRef, error) {
	rows, err := db.Query(`
		SELECT workspace_id, guardrail_name, position, enabled
		FROM workspace_guardrails
		WHERE workspace_id = ?
		ORDER BY position ASC, guardrail_name ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: read workspace %s guardrails: %w", workspaceID, err)
	}
	defer rows.Close()
	var out []fleet.WorkspaceGuardrailRef
	for rows.Next() {
		var ref fleet.WorkspaceGuardrailRef
		var enabled int
		if err := rows.Scan(&ref.WorkspaceID, &ref.GuardrailName, &ref.Position, &enabled); err != nil {
			return nil, fmt.Errorf("store: read workspace %s guardrails: %w", workspaceID, err)
		}
		ref.Enabled = intToBool(enabled)
		out = append(out, ref)
	}
	return out, rows.Err()
}

// ReplaceWorkspaceGuardrails replaces the workspace's selected guardrail
// references after validating each reference points at the global catalog.
func ReplaceWorkspaceGuardrails(db *sql.DB, workspace string, refs []fleet.WorkspaceGuardrailRef) ([]fleet.WorkspaceGuardrailRef, error) {
	workspaceID, err := ResolveWorkspaceID(db, workspace)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: replace workspace %s guardrails: begin: %w", workspaceID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM workspace_guardrails WHERE workspace_id=?", workspaceID); err != nil {
		return nil, fmt.Errorf("store: replace workspace %s guardrails: clear: %w", workspaceID, err)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		name := fleet.NormalizeGuardrailName(ref.GuardrailName)
		if name == "" {
			return nil, &ErrValidation{Msg: "guardrail_name is required"}
		}
		if _, ok := seen[name]; ok {
			return nil, &ErrValidation{Msg: fmt.Sprintf("workspace %q references guardrail %q more than once", workspaceID, name)}
		}
		seen[name] = struct{}{}
		var exists int
		err := tx.QueryRow("SELECT 1 FROM guardrails WHERE name=?", name).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &ErrValidation{Msg: fmt.Sprintf("workspace %q references unknown guardrail %q", workspaceID, name)}
		}
		if err != nil {
			return nil, fmt.Errorf("store: replace workspace %s guardrails: validate %s: %w", workspaceID, name, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
			VALUES (?, ?, ?, ?)`,
			workspaceID, name, ref.Position, boolToInt(ref.Enabled),
		); err != nil {
			return nil, fmt.Errorf("store: replace workspace %s guardrails: insert %s: %w", workspaceID, name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: replace workspace %s guardrails: commit: %w", workspaceID, err)
	}
	return ReadWorkspaceGuardrails(db, workspaceID)
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
