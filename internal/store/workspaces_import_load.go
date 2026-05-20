package store

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importWorkspaces(tx *sql.Tx, workspaces []fleet.Workspace) error {
	for _, w := range workspaces {
		if w.ID == "" {
			id, err := derivedEntityID("", w.Name)
			if err != nil {
				return fmt.Errorf("store import: workspace %q: %w", w.Name, err)
			}
			w.ID = id
		}
		if w.ID == "" || w.Name == "" {
			return fmt.Errorf("store import: workspace requires id or name")
		}
		if err := validateEntityID(w.ID); err != nil {
			return fmt.Errorf("store import: workspace %q: %w", w.Name, err)
		}
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)`, w.ID).Scan(&exists); err != nil {
			return fmt.Errorf("store import: check workspace %s: %w", w.ID, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO workspaces (id, name, description, runner_image, updated_at)
			VALUES (?, ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				description = excluded.description,
				runner_image = excluded.runner_image,
				updated_at = datetime('now')`,
			w.ID, w.Name, w.Description, strings.TrimSpace(w.RunnerImage),
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: workspace name %q is already used by another workspace id", w.Name)
			}
			return fmt.Errorf("store import: upsert workspace %s: %w", w.ID, err)
		}
		if !exists {
			if err := seedWorkspaceGuardrails(tx, w.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedWorkspaceGuardrails(tx *sql.Tx, workspaceID string) error {
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
		SELECT ?, id, position, enabled
		FROM guardrails
		WHERE is_builtin = 1 AND workspace_id IS NULL AND repo IS NULL`,
		workspaceID,
	); err != nil {
		return fmt.Errorf("store import: seed workspace %s guardrails: %w", workspaceID, err)
	}
	return nil
}

func importWorkspaceGuardrails(tx *sql.Tx, workspaces []fleet.Workspace) error {
	for _, w := range workspaces {
		if w.Guardrails == nil {
			continue
		}
		workspaceID := strings.TrimSpace(w.ID)
		if workspaceID == "" {
			id, err := derivedEntityID("", w.Name)
			if err != nil {
				return fmt.Errorf("store import: workspace %q guardrails: %w", w.Name, err)
			}
			workspaceID = id
		} else {
			workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
		}
		// Omitted guardrails preserve existing refs; an explicit empty list clears them.
		if err := replaceWorkspaceGuardrailsTx(tx, workspaceID, w.Guardrails); err != nil {
			return err
		}
	}
	return nil
}

func replaceWorkspaceGuardrailsTx(tx *sql.Tx, workspaceID string, refs []fleet.WorkspaceGuardrailRef) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)`, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("store import: check workspace %s: %w", workspaceID, err)
	}
	if !exists {
		return &ErrValidation{Msg: fmt.Sprintf("workspace %q not found", workspaceID)}
	}
	if _, err := tx.Exec(`DELETE FROM workspace_guardrails WHERE workspace_id = ?`, workspaceID); err != nil {
		return fmt.Errorf("store import: replace workspace %s guardrails: %w", workspaceID, err)
	}
	seen := map[string]struct{}{}
	for i, ref := range refs {
		name := fleet.NormalizeGuardrailName(ref.GuardrailName)
		if name == "" {
			return &ErrValidation{Msg: fmt.Sprintf("workspace %q guardrail name is required", workspaceID)}
		}
		if _, ok := seen[name]; ok {
			return &ErrValidation{Msg: fmt.Sprintf("workspace %q references guardrail %q more than once", workspaceID, name)}
		}
		seen[name] = struct{}{}
		id, err := resolveWorkspaceGuardrailRef(tx, workspaceID, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ErrValidation{Msg: fmt.Sprintf("workspace %q references unknown guardrail %q", workspaceID, name)}
			}
			return fmt.Errorf("store import: validate workspace %s guardrail %s: %w", workspaceID, name, err)
		}
		position := ref.Position
		if position == 0 {
			position = i
		}
		if _, err := tx.Exec(`
			INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
			VALUES (?, ?, ?, ?)`,
			workspaceID, id, position, boolToInt(ref.Enabled),
		); err != nil {
			return fmt.Errorf("store import: insert workspace %s guardrail %s: %w", workspaceID, name, err)
		}
	}
	return nil
}

func resolveWorkspaceGuardrailRef(q querier, workspaceID, ref string) (string, error) {
	ref = fleet.NormalizeGuardrailName(ref)
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	rows, err := q.Query(`
		SELECT id, ref, name
		FROM guardrails
		WHERE repo IS NULL
		  AND (id = ? OR ref = ? OR name = ?)
		  AND (workspace_id IS NULL OR workspace_id = ?)
		ORDER BY
			CASE WHEN id = ? OR ref = ? THEN 0 ELSE 1 END,
			CASE WHEN workspace_id IS NULL THEN 0 ELSE 1 END`,
		ref, ref, ref, workspaceID, ref, ref,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type match struct {
		id    string
		ref   string
		name  string
		exact bool
	}
	var matches []match
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.id, &m.ref, &m.name); err != nil {
			return "", err
		}
		m.exact = m.id == ref || m.ref == ref
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", sql.ErrNoRows
	}
	if matches[0].exact {
		return matches[0].id, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous guardrail %q in workspace %q; use guardrail id", ref, workspaceID)
	}
	return matches[0].id, nil
}

func importReferencedWorkspaces(tx *sql.Tx, agents []fleet.Agent, repos []fleet.Repo) error {
	seen := map[string]struct{}{fleet.DefaultWorkspaceID: {}}
	for _, a := range agents {
		id := strings.TrimSpace(a.WorkspaceID)
		if id == "" {
			id = fleet.DefaultWorkspaceID
		}
		seen[id] = struct{}{}
	}
	for _, r := range repos {
		id := strings.TrimSpace(r.WorkspaceID)
		if id == "" {
			id = fleet.DefaultWorkspaceID
		}
		seen[id] = struct{}{}
	}
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: workspace %q: %w", id, err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO workspaces (id, name, description, runner_image, updated_at)
			VALUES (?, ?, '', '', datetime('now'))`,
			id, workspaceNameFromID(id),
		)
		if err != nil {
			return fmt.Errorf("store import: ensure workspace %s: %w", id, err)
		}
		if inserted, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store import: check workspace %s insert result: %w", id, err)
		} else if inserted > 0 {
			if err := seedWorkspaceGuardrails(tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func importReferencedCatalogWorkspaces(tx *sql.Tx, prompts []fleet.Prompt, skills map[string]fleet.Skill, guardrails []fleet.Guardrail) error {
	seen := map[string]struct{}{}
	for _, p := range prompts {
		if id := strings.TrimSpace(p.WorkspaceID); id != "" {
			seen[fleet.NormalizeWorkspaceID(id)] = struct{}{}
		}
	}
	for _, s := range skills {
		if id := strings.TrimSpace(s.WorkspaceID); id != "" {
			seen[fleet.NormalizeWorkspaceID(id)] = struct{}{}
		}
	}
	for _, g := range guardrails {
		if id := strings.TrimSpace(g.WorkspaceID); id != "" {
			seen[fleet.NormalizeWorkspaceID(id)] = struct{}{}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: workspace %q: %w", id, err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO workspaces (id, name, description, runner_image, updated_at)
			VALUES (?, ?, '', '', datetime('now'))`,
			id, workspaceNameFromID(id),
		)
		if err != nil {
			return fmt.Errorf("store import: ensure catalog workspace %s: %w", id, err)
		}
		if inserted, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store import: check catalog workspace %s insert result: %w", id, err)
		} else if inserted > 0 {
			if err := seedWorkspaceGuardrails(tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func workspaceNameFromID(id string) string {
	if id == fleet.DefaultWorkspaceID {
		return "Default"
	}
	return id
}

func loadWorkspaces(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT id,name,description,runner_image FROM workspaces ORDER BY name")
	if err != nil {
		return fmt.Errorf("store load: query workspaces: %w", err)
	}
	defer rows.Close()
	var workspaces []fleet.Workspace
	for rows.Next() {
		var w fleet.Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.RunnerImage); err != nil {
			return fmt.Errorf("store load: scan workspace: %w", err)
		}
		workspaces = append(workspaces, w)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store load: close workspaces: %w", err)
	}
	for i := range workspaces {
		refs, err := readWorkspaceGuardrails(db, workspaces[i].ID)
		if err != nil {
			return err
		}
		workspaces[i].Guardrails = refs
	}
	cfg.Workspaces = workspaces
	return nil
}
