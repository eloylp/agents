package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/fleet"
)

func ListPromptVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: list prompt version references: %w", err)
	}
	defer tx.Rollback()

	promptID, currentVersionID, err := promptVersionAsset(tx, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`
		SELECT COALESCE(workspace_id, ''), name, prompt_version_id
		FROM agents
		WHERE prompt_id=? AND (prompt_version_id=? OR (NULLIF(prompt_version_id, '') IS NULL AND ?=?))
		ORDER BY COALESCE(workspace_id, ''), name`,
		promptID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list prompt version references: %w", err)
	}
	refs, err := scanCatalogVersionReferences(rows, "agent", "prompt", versionID)
	if closeErr := rows.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("store: list prompt version references: %w", closeErr)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: list prompt version references: %w", err)
	}
	return refs, nil
}

func UpgradePromptVersionReferences(db *sql.DB, ref, fromVersionID, toVersionID string) (fleet.CatalogVersionRolloutResult, error) {
	return upgradeCatalogVersionReferences(db, ref, fromVersionID, toVersionID, catalogVersionRolloutSpec{
		versionTable: "prompt_versions",
		assetColumn:  "prompt_id",
		assetTable:   "prompts",
		refTable:     "agents",
		refColumn:    "prompt_id",
		pinColumn:    "prompt_version_id",
	})
}

func ListSkillVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: list skill version references: %w", err)
	}
	defer tx.Rollback()

	skillID, currentVersionID, err := skillVersionAsset(tx, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`
		SELECT COALESCE(a.workspace_id, ''), a.name, ask.skill_version_id
		FROM agent_skills ask
		JOIN agents a ON a.id = ask.agent_id
		WHERE ask.skill_id=? AND (ask.skill_version_id=? OR (NULLIF(ask.skill_version_id, '') IS NULL AND ?=?))
		ORDER BY COALESCE(a.workspace_id, ''), a.name`,
		skillID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list skill version references: %w", err)
	}
	refs, err := scanCatalogVersionReferences(rows, "agent", "skill", versionID)
	if closeErr := rows.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("store: list skill version references: %w", closeErr)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: list skill version references: %w", err)
	}
	return refs, nil
}

func UpgradeSkillVersionReferences(db *sql.DB, ref, fromVersionID, toVersionID string) (fleet.CatalogVersionRolloutResult, error) {
	return upgradeCatalogVersionReferences(db, ref, fromVersionID, toVersionID, catalogVersionRolloutSpec{
		versionTable: "skill_versions",
		assetColumn:  "skill_id",
		assetTable:   "skills",
		refTable:     "agent_skills",
		refColumn:    "skill_id",
		pinColumn:    "skill_version_id",
	})
}

func ListGuardrailVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: list guardrail version references: %w", err)
	}
	defer tx.Rollback()

	guardrailID, currentVersionID, err := guardrailVersionAsset(tx, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`
		SELECT workspace_id, workspace_id, guardrail_version_id
		FROM workspace_guardrails
		WHERE guardrail_name=? AND (guardrail_version_id=? OR (NULLIF(guardrail_version_id, '') IS NULL AND ?=?))
		ORDER BY workspace_id, guardrail_name`,
		guardrailID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list guardrail version references: %w", err)
	}
	refs, err := scanCatalogVersionReferences(rows, "workspace", "guardrail", versionID)
	if closeErr := rows.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("store: list guardrail version references: %w", closeErr)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: list guardrail version references: %w", err)
	}
	return refs, nil
}

func UpgradeGuardrailVersionReferences(db *sql.DB, ref, fromVersionID, toVersionID string) (fleet.CatalogVersionRolloutResult, error) {
	return upgradeCatalogVersionReferences(db, ref, fromVersionID, toVersionID, catalogVersionRolloutSpec{
		versionTable: "guardrail_versions",
		assetColumn:  "guardrail_id",
		assetTable:   "guardrails",
		refTable:     "workspace_guardrails",
		refColumn:    "guardrail_name",
		pinColumn:    "guardrail_version_id",
	})
}

func scanCatalogVersionReferences(rows *sql.Rows, kind, reference, versionID string) ([]fleet.CatalogVersionReference, error) {
	refs := []fleet.CatalogVersionReference{}
	for rows.Next() {
		var ref fleet.CatalogVersionReference
		var referencedVersionID sql.NullString
		if err := rows.Scan(&ref.WorkspaceID, &ref.Name, &referencedVersionID); err != nil {
			return nil, fmt.Errorf("store: scan catalog version reference: %w", err)
		}
		ref.Kind = kind
		ref.Reference = reference
		ref.VersionID = versionID
		ref.Tracking = !referencedVersionID.Valid || referencedVersionID.String == ""
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

type catalogVersionRolloutSpec struct {
	versionTable string
	assetColumn  string
	assetTable   string
	refTable     string
	refColumn    string
	pinColumn    string
}

func upgradeCatalogVersionReferences(db *sql.DB, ref, fromVersionID, toVersionID string, spec catalogVersionRolloutSpec) (fleet.CatalogVersionRolloutResult, error) {
	tx, err := db.Begin()
	if err != nil {
		return fleet.CatalogVersionRolloutResult{}, fmt.Errorf("store: upgrade catalog version references: %w", err)
	}
	defer tx.Rollback()

	assetID, err := catalogAssetInternalID(tx, spec.assetTable, ref)
	if err != nil {
		return fleet.CatalogVersionRolloutResult{}, err
	}
	if err := ensureCatalogVersionBelongsToAsset(tx, spec, assetID, fromVersionID, false); err != nil {
		return fleet.CatalogVersionRolloutResult{}, err
	}
	if err := ensureCatalogVersionBelongsToAsset(tx, spec, assetID, toVersionID, true); err != nil {
		return fleet.CatalogVersionRolloutResult{}, err
	}
	if fromVersionID == toVersionID {
		if err := tx.Commit(); err != nil {
			return fleet.CatalogVersionRolloutResult{}, fmt.Errorf("store: upgrade catalog version references: %w", err)
		}
		return fleet.CatalogVersionRolloutResult{Updated: 0}, nil
	}
	res, err := tx.Exec(`
		UPDATE `+spec.refTable+`
		SET `+spec.pinColumn+`=?
		WHERE `+spec.refColumn+`=? AND `+spec.pinColumn+`=?`,
		toVersionID, assetID, fromVersionID)
	if err != nil {
		return fleet.CatalogVersionRolloutResult{}, fmt.Errorf("store: upgrade catalog version references: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fleet.CatalogVersionRolloutResult{}, fmt.Errorf("store: upgrade catalog version references: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fleet.CatalogVersionRolloutResult{}, fmt.Errorf("store: upgrade catalog version references: %w", err)
	}
	return fleet.CatalogVersionRolloutResult{Updated: updated}, nil
}

func catalogAssetInternalID(q querier, assetTable, ref string) (string, error) {
	switch assetTable {
	case "prompts":
		return promptInternalID(q, ref)
	case "skills":
		return skillInternalID(q, ref)
	case "guardrails":
		return guardrailInternalID(q, ref)
	default:
		return "", &ErrValidation{Msg: "unknown catalog asset type"}
	}
}

func ensureCatalogVersionBelongsToAsset(q querier, spec catalogVersionRolloutSpec, assetID, versionID string, requirePublished bool) error {
	query := `SELECT ` + spec.assetColumn + ` FROM ` + spec.versionTable + ` WHERE id=?`
	if requirePublished {
		query += ` AND state='published'`
	}
	var got string
	err := q.QueryRow(query, versionID).Scan(&got)
	if err != nil {
		return versionReadErr("catalog", versionID, err)
	}
	if got != assetID {
		return &ErrNotFound{Msg: fmt.Sprintf("catalog version %q not found for %q", versionID, assetID)}
	}
	return nil
}

func promptVersionAsset(q querier, ref, versionID string) (assetID, currentVersionID string, err error) {
	promptID, err := promptInternalID(q, ref)
	if err != nil {
		return "", "", err
	}
	return catalogVersionAsset(q, "prompt_versions", "prompt_id", "prompts", promptID, versionID)
}

func skillVersionAsset(q querier, ref, versionID string) (assetID, currentVersionID string, err error) {
	skillID, err := skillInternalID(q, ref)
	if err != nil {
		return "", "", err
	}
	return catalogVersionAsset(q, "skill_versions", "skill_id", "skills", skillID, versionID)
}

func guardrailVersionAsset(q querier, ref, versionID string) (assetID, currentVersionID string, err error) {
	guardrailID, err := guardrailInternalID(q, ref)
	if err != nil {
		return "", "", err
	}
	return catalogVersionAsset(q, "guardrail_versions", "guardrail_id", "guardrails", guardrailID, versionID)
}

func catalogVersionAsset(q querier, versionTable, assetColumn, assetTable, expectedAssetID, versionID string) (assetID, currentVersionID string, err error) {
	err = q.QueryRow(`
		SELECT v.`+assetColumn+`, COALESCE(a.current_version_id, '')
		FROM `+versionTable+` v
		JOIN `+assetTable+` a ON a.id = v.`+assetColumn+`
		WHERE v.id=?`, versionID).Scan(&assetID, &currentVersionID)
	if err != nil {
		return "", "", versionReadErr("catalog", versionID, err)
	}
	if assetID != expectedAssetID {
		return "", "", &ErrNotFound{Msg: fmt.Sprintf("catalog version %q not found for %q", versionID, expectedAssetID)}
	}
	return assetID, currentVersionID, nil
}
