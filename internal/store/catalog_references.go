package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/fleet"
)

func ListPromptVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	promptID, currentVersionID, err := promptVersionAsset(db, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT COALESCE(workspace_id, ''), name, prompt_version_id
		FROM agents
		WHERE prompt_id=? AND (prompt_version_id=? OR (NULLIF(prompt_version_id, '') IS NULL AND ?=?))
		ORDER BY COALESCE(workspace_id, ''), name`,
		promptID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list prompt version references: %w", err)
	}
	defer rows.Close()
	return scanCatalogVersionReferences(rows, "agent", "prompt")
}

func ListSkillVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	skillID, currentVersionID, err := skillVersionAsset(db, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT COALESCE(a.workspace_id, ''), a.name, ask.skill_version_id
		FROM agent_skills ask
		JOIN agents a ON a.id = ask.agent_id
		WHERE ask.skill_id=? AND (ask.skill_version_id=? OR (NULLIF(ask.skill_version_id, '') IS NULL AND ?=?))
		ORDER BY COALESCE(a.workspace_id, ''), a.name`,
		skillID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list skill version references: %w", err)
	}
	defer rows.Close()
	return scanCatalogVersionReferences(rows, "agent", "skill")
}

func ListGuardrailVersionReferences(db *sql.DB, ref, versionID string) ([]fleet.CatalogVersionReference, error) {
	guardrailID, currentVersionID, err := guardrailVersionAsset(db, ref, versionID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT workspace_id, workspace_id, guardrail_version_id
		FROM workspace_guardrails
		WHERE guardrail_name=? AND (guardrail_version_id=? OR (NULLIF(guardrail_version_id, '') IS NULL AND ?=?))
		ORDER BY workspace_id, guardrail_name`,
		guardrailID, versionID, currentVersionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("store: list guardrail version references: %w", err)
	}
	defer rows.Close()
	return scanCatalogVersionReferences(rows, "workspace", "guardrail")
}

func scanCatalogVersionReferences(rows *sql.Rows, kind, reference string) ([]fleet.CatalogVersionReference, error) {
	var refs []fleet.CatalogVersionReference
	for rows.Next() {
		var ref fleet.CatalogVersionReference
		var versionID sql.NullString
		if err := rows.Scan(&ref.WorkspaceID, &ref.Name, &versionID); err != nil {
			return nil, fmt.Errorf("store: scan catalog version reference: %w", err)
		}
		ref.Kind = kind
		ref.Reference = reference
		ref.Tracking = !versionID.Valid || versionID.String == ""
		refs = append(refs, ref)
	}
	return refs, rows.Err()
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
