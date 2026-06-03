package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

func EnsureSelfImprovementCatalogRefAvailable(q sqlExec, assetType, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return &ErrValidation{Msg: "proposal bundle create-new item ref is required"}
	}
	var exists bool
	var err error
	switch assetType {
	case "prompt":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM prompts WHERE ref=?)`, ref).Scan(&exists)
	case "skill":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM skills WHERE ref=?)`, ref).Scan(&exists)
	case "guardrail":
		err = q.QueryRow(`SELECT EXISTS(SELECT 1 FROM guardrails WHERE ref=?)`, ref).Scan(&exists)
	default:
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return fmt.Errorf("store: check proposal bundle create-new ref: %w", err)
	}
	if exists {
		return &ErrConflict{Msg: fmt.Sprintf("%s %q already exists", assetType, ref)}
	}
	return nil
}

func CurrentSelfImprovementCatalogVersionID(q querier, assetType, assetID string) (string, error) {
	var id string
	var err error
	switch strings.TrimSpace(assetType) {
	case "prompt":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM prompts WHERE id=? OR ref=?`, assetID, assetID).Scan(&id)
	case "skill":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM skills WHERE id=? OR ref=? OR name=?`, assetID, assetID, fleet.NormalizeSkillName(assetID)).Scan(&id)
	case "guardrail":
		err = q.QueryRow(`SELECT COALESCE(current_version_id, '') FROM guardrails WHERE id=? OR ref=? OR name=?`, assetID, assetID, fleet.NormalizeGuardrailName(assetID)).Scan(&id)
	default:
		return "", &ErrValidation{Msg: fmt.Sprintf("proposal bundle asset type %q is unsupported", assetType)}
	}
	if err != nil {
		return "", catalogReadErr(assetType, assetID, err)
	}
	if id == "" {
		return "", &ErrValidation{Msg: fmt.Sprintf("%s %q has no current version", assetType, assetID)}
	}
	return id, nil
}

func ValidateSelfImprovementBundleScope(q querier, raw, currentWorkspace string) error {
	raw = strings.TrimSpace(raw)
	scope := strings.ToLower(raw)
	if scope == "global" || scope == "workspace" {
		return nil
	}
	workspace, repo := ParseSelfImprovementBundleScope(raw, currentWorkspace)
	if workspace == "" && repo == "" {
		return nil
	}
	var exists bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=?)`, workspace).Scan(&exists); err != nil {
		return fmt.Errorf("store: validate proposal bundle scope workspace: %w", err)
	}
	if !exists {
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle scope workspace %q does not exist", workspace)}
	}
	if repo == "" {
		return nil
	}
	if len(strings.Split(raw, "/")) != 3 || repo == "" {
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle repo scope %q is invalid", raw)}
	}
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)`, workspace, repo).Scan(&exists); err != nil {
		return fmt.Errorf("store: validate proposal bundle scope repo: %w", err)
	}
	if !exists {
		return &ErrValidation{Msg: fmt.Sprintf("proposal bundle scope repo %q does not exist", repo)}
	}
	return nil
}

func ParseSelfImprovementBundleScope(raw, currentWorkspace string) (workspace, repo string) {
	raw = strings.TrimSpace(raw)
	scope := strings.ToLower(raw)
	if raw == "" || scope == "global" {
		return "", ""
	}
	if scope == "workspace" {
		return fleet.NormalizeWorkspaceID(currentWorkspace), ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) >= 3 {
		return fleet.NormalizeWorkspaceID(parts[0]), fleet.NormalizeRepoName(strings.Join(parts[1:], "/"))
	}
	return fleet.NormalizeWorkspaceID(raw), ""
}

func ReadSelfImprovementPrompt(q querier, ref string) (fleet.Prompt, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt id is required"}
	}
	var p fleet.Prompt
	err := q.QueryRow(`
		SELECT p.ref, COALESCE(p.workspace_id, ''), COALESCE(p.repo, ''), p.name, p.description, p.content,
		       COALESCE(pv.id, ''), COALESCE(pv.version_number, 0)
		FROM prompts p
		LEFT JOIN prompt_versions pv ON pv.id = p.current_version_id
		WHERE p.id=? OR p.ref=?`, ref, ref).
		Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content, &p.VersionID, &p.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s: %w", ref, err)
	}
	return p, nil
}

func ReadSelfImprovementSkill(q querier, ref string) (fleet.Skill, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Skill{}, &ErrValidation{Msg: "skill id is required"}
	}
	var skill fleet.Skill
	err := q.QueryRow(`
		SELECT s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name, s.prompt,
		       COALESCE(sv.id, ''), COALESCE(sv.version_number, 0)
		FROM skills s
		LEFT JOIN skill_versions sv ON sv.id = s.current_version_id
		WHERE s.id=? OR s.ref=? OR s.name=?`, ref, ref, fleet.NormalizeSkillName(ref)).
		Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt, &skill.VersionID, &skill.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, &ErrNotFound{Msg: fmt.Sprintf("skill %q not found", ref)}
	}
	if err != nil {
		return fleet.Skill{}, fmt.Errorf("store: read skill %s: %w", ref, err)
	}
	return skill, nil
}

func ReadSelfImprovementGuardrail(q querier, ref string) (fleet.Guardrail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Guardrail{}, &ErrValidation{Msg: "guardrail id is required"}
	}
	var g fleet.Guardrail
	var enabled int
	err := q.QueryRow(`
		SELECT ref, COALESCE(workspace_id, ''), name, description, content, enabled, position, COALESCE(current_version_id, '')
		FROM guardrails
		WHERE id=? OR ref=? OR name=?`, ref, ref, fleet.NormalizeGuardrailName(ref)).
		Scan(&g.ID, &g.WorkspaceID, &g.Name, &g.Description, &g.Content, &enabled, &g.Position, &g.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Guardrail{}, &ErrNotFound{Msg: fmt.Sprintf("guardrail %q not found", ref)}
	}
	if err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: read guardrail %s: %w", ref, err)
	}
	g.Enabled = enabled != 0
	return g, nil
}

func ReadSelfImprovementCatalogVersion(q querier, targetType, versionID string) (fleet.CatalogVersion, error) {
	return readSelfImprovementProposalBaseVersion(q, targetType, versionID)
}
