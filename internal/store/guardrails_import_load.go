package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// importGuardrails upserts each guardrail using the same ON CONFLICT shape
// as store.UpsertGuardrail: existing rows have their description, content,
// enabled, and position fields updated; is_builtin and default_content are
// preserved (they are migration-controlled and intentionally not editable
// from YAML). Operator-added rows are inserted with default_content NULL
// and is_builtin = 0.
func importGuardrails(tx *sql.Tx, guardrails []fleet.Guardrail) error {
	for _, g := range guardrails {
		fleet.NormalizeGuardrail(&g)
		if g.WorkspaceID == "" && g.Repo != "" {
			return fmt.Errorf("store import: guardrail %q repo scope requires workspace_id", g.Name)
		}
		if g.ID == "" {
			var existingID string
			err := queryCatalogIDByScopeName(tx, "guardrails", g.WorkspaceID, g.Repo, g.Name).Scan(&existingID)
			if err == nil {
				g.ID = existingID
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("store import: read guardrail %s: %w", g.Name, err)
			}
		}
		if g.ID == "" {
			id, err := derivedCatalogID("guardrail_", g.WorkspaceID, g.Repo, g.Name)
			if err != nil {
				return fmt.Errorf("store import: guardrail %q: %w", g.Name, err)
			}
			g.ID = id
		}
		if g.Name == "" || g.Content == "" {
			return fmt.Errorf("store import: guardrail requires name and content (got name=%q)", g.Name)
		}
		if err := validateEntityID(g.ID); err != nil {
			return fmt.Errorf("store import: guardrail %q: %w", g.Name, err)
		}
		if isReservedGuardrailName(g.Name) {
			return fmt.Errorf("store import: guardrail name %q is reserved for runtime-generated policy", g.Name)
		}
		enabled := boolToInt(g.Enabled)
		if _, err := tx.Exec(`
			INSERT INTO guardrails (id, workspace_id, repo, name, description, content, enabled, position, updated_at)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				description = excluded.description,
				content     = excluded.content,
				enabled     = excluded.enabled,
				position    = excluded.position,
				updated_at  = datetime('now')`,
			g.ID, g.WorkspaceID, g.Repo, g.Name, g.Description, g.Content, enabled, g.Position,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: guardrail name %q is already used by another guardrail in that scope", g.Name)
			}
			return fmt.Errorf("store import: upsert guardrail %s: %w", g.Name, err)
		}
	}
	return nil
}

func loadGuardrails(db *sql.DB, cfg *config.Config) error {
	rows, err := ReadAllGuardrails(db)
	if err != nil {
		return fmt.Errorf("store load: read guardrails: %w", err)
	}
	cfg.Guardrails = rows
	return nil
}
