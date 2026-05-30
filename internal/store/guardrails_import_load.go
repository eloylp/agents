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
		if err := ensureCatalogScope(tx, "guardrail", g.WorkspaceID, ""); err != nil {
			return err
		}
		if g.ID == "" {
			var existingID, existingRef string
			err := queryGuardrailRefByScopeName(tx, g.WorkspaceID, g.Name).Scan(&existingID, &existingRef)
			if err == nil {
				g.ID = existingRef
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("store import: read guardrail %s: %w", g.Name, err)
			}
		}
		if g.ID == "" {
			id, err := derivedCatalogID("guardrail_", g.WorkspaceID, "", g.Name)
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
		internalID, _, err := resolveCatalogID(tx, "guardrails", g.ID)
		if errors.Is(err, sql.ErrNoRows) {
			internalID, err = newCatalogInternalID("guardrail_")
		}
		if err != nil {
			return fmt.Errorf("store import: guardrail %q: resolve id: %w", g.Name, err)
		}
		if isReservedGuardrailName(g.Name) {
			return fmt.Errorf("store import: guardrail name %q is reserved for runtime-generated policy", g.Name)
		}
		enabled := boolToInt(g.Enabled)
		if _, err := tx.Exec(`
			INSERT INTO guardrails (id, ref, workspace_id, name, description, content, enabled, position, updated_at)
			VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, datetime('now'))
			ON CONFLICT(ref) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				name = excluded.name,
				description = excluded.description,
				content     = excluded.content,
				enabled     = excluded.enabled,
				position    = excluded.position,
				updated_at  = datetime('now')`,
			internalID, g.ID, g.WorkspaceID, g.Name, g.Description, g.Content, enabled, g.Position,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: guardrail name %q is already used by another guardrail in that scope", g.Name)
			}
			return fmt.Errorf("store import: upsert guardrail %s: %w", g.Name, err)
		}
		version, err := publishGuardrailVersionTx(tx, internalID, g)
		if err != nil {
			return fmt.Errorf("store import: publish guardrail %s version: %w", g.Name, err)
		}
		if len(g.Versions) > 0 {
			version, err = replaceGuardrailVersionSnapshotsTx(tx, internalID, g.Versions)
			if err != nil {
				return fmt.Errorf("store import: replace guardrail %s versions: %w", g.Name, err)
			}
		}
		if err := applyGuardrailCurrentVersionTx(tx, internalID, version.ID); err != nil {
			return fmt.Errorf("store import: update guardrail %s current version: %w", g.Name, err)
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
