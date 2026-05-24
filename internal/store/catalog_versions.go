package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

func catalogBodyHash(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func publishPromptVersionTx(tx *sql.Tx, promptID, description, content string) (fleet.CatalogVersion, error) {
	version, err := nextCatalogVersion(tx, "prompt_versions", "prompt_id", promptID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	id, err := newCatalogInternalID("promptver_")
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	baseID, err := currentVersionID(tx, "prompts", promptID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO prompt_versions
			(id, prompt_id, version_number, state, description, content, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, 'published', ?, ?, 'manual', NULLIF(?, ''), ?, datetime('now'), datetime('now'))`,
		id, promptID, version, description, content, baseID, catalogBodyHash(description, content),
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: publish prompt version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: promptID, Version: version, State: "published", SourceType: "manual", BaseVersionID: baseID, BodyHash: catalogBodyHash(description, content)}, nil
}

func publishSkillVersionTx(tx *sql.Tx, skillID, prompt string) (fleet.CatalogVersion, error) {
	version, err := nextCatalogVersion(tx, "skill_versions", "skill_id", skillID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	id, err := newCatalogInternalID("skillver_")
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	baseID, err := currentVersionID(tx, "skills", skillID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO skill_versions
			(id, skill_id, version_number, state, prompt, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, 'published', ?, 'manual', NULLIF(?, ''), ?, datetime('now'), datetime('now'))`,
		id, skillID, version, prompt, baseID, catalogBodyHash(prompt),
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: publish skill version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: skillID, Version: version, State: "published", SourceType: "manual", BaseVersionID: baseID, BodyHash: catalogBodyHash(prompt)}, nil
}

func publishGuardrailVersionTx(exec sqlExec, guardrailID string, g fleet.Guardrail) (fleet.CatalogVersion, error) {
	version, err := nextCatalogVersion(exec, "guardrail_versions", "guardrail_id", guardrailID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	id, err := newCatalogInternalID("guardrailver_")
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	baseID, err := currentVersionID(exec, "guardrails", guardrailID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	hash := catalogBodyHash(g.Description, g.Content, fmt.Sprint(g.Enabled), fmt.Sprint(g.Position))
	if _, err := exec.Exec(`
		INSERT INTO guardrail_versions
			(id, guardrail_id, version_number, state, description, content, enabled, position, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, 'published', ?, ?, ?, ?, 'manual', NULLIF(?, ''), ?, datetime('now'), datetime('now'))`,
		id, guardrailID, version, g.Description, g.Content, boolToInt(g.Enabled), g.Position, baseID, hash,
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: publish guardrail version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: guardrailID, Version: version, State: "published", SourceType: "manual", BaseVersionID: baseID, BodyHash: hash}, nil
}

func nextCatalogVersion(q querier, table, column, assetID string) (int, error) {
	var max sql.NullInt64
	if err := q.QueryRow("SELECT MAX(version_number) FROM "+table+" WHERE "+column+"=?", assetID).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return 1, nil
	}
	return int(max.Int64) + 1, nil
}

func currentVersionID(q querier, table, id string) (string, error) {
	var current sql.NullString
	if err := q.QueryRow("SELECT COALESCE(current_version_id, '') FROM "+table+" WHERE id=?", id).Scan(&current); err != nil {
		return "", fmt.Errorf("store: read current version for %s %s: %w", table, id, err)
	}
	if current.Valid {
		return strings.TrimSpace(current.String), nil
	}
	return "", nil
}
