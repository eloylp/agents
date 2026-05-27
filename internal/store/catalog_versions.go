package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
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

func CreatePromptDraftTx(tx *sql.Tx, ref, description, content string) (fleet.CatalogVersion, error) {
	promptID, err := promptInternalID(tx, ref)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	version, err := createPromptVersionTx(tx, promptID, "draft", description, content)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	return version, nil
}

func ListPromptVersions(db *sql.DB, ref string) ([]fleet.CatalogVersion, error) {
	promptID, err := promptInternalID(db, ref)
	if err != nil {
		return nil, err
	}
	return listCatalogVersions(db, "prompt_versions", "prompt_id", promptID)
}

func ListPromptVersionSnapshots(q querier, ref string) ([]fleet.CatalogVersion, error) {
	promptID, err := promptInternalID(q, ref)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`
		SELECT id, prompt_id, version_number, state, description, content, source_type, source_ref, author, changelog,
		       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
		FROM prompt_versions
		WHERE prompt_id=?
		ORDER BY version_number ASC`, promptID)
	if err != nil {
		return nil, fmt.Errorf("store: list prompt version snapshots for %s: %w", promptID, err)
	}
	defer rows.Close()
	var versions []fleet.CatalogVersion
	for rows.Next() {
		var version fleet.CatalogVersion
		if err := rows.Scan(
			&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
			&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
			&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan prompt version snapshot: %w", err)
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func ListSkillVersions(db *sql.DB, ref string) ([]fleet.CatalogVersion, error) {
	skillID, err := skillInternalID(db, ref)
	if err != nil {
		return nil, err
	}
	return listCatalogVersions(db, "skill_versions", "skill_id", skillID)
}

func ListSkillVersionSnapshots(q querier, ref string) ([]fleet.CatalogVersion, error) {
	skillID, err := skillInternalID(q, ref)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`
		SELECT id, skill_id, version_number, state, prompt, source_type, source_ref, author, changelog,
		       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
		FROM skill_versions
		WHERE skill_id=?
		ORDER BY version_number ASC`, skillID)
	if err != nil {
		return nil, fmt.Errorf("store: list skill version snapshots for %s: %w", skillID, err)
	}
	defer rows.Close()
	var versions []fleet.CatalogVersion
	for rows.Next() {
		var version fleet.CatalogVersion
		if err := rows.Scan(
			&version.ID, &version.AssetID, &version.Version, &version.State, &version.Prompt,
			&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
			&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan skill version snapshot: %w", err)
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func ListGuardrailVersions(db *sql.DB, ref string) ([]fleet.CatalogVersion, error) {
	guardrailID, err := guardrailInternalID(db, ref)
	if err != nil {
		return nil, err
	}
	return listCatalogVersions(db, "guardrail_versions", "guardrail_id", guardrailID)
}

func ListGuardrailVersionSnapshots(q querier, ref string) ([]fleet.CatalogVersion, error) {
	guardrailID, err := guardrailInternalID(q, ref)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`
		SELECT id, guardrail_id, version_number, state, description, content, enabled, position,
		       source_type, source_ref, author, changelog, COALESCE(base_version_id, ''), body_hash,
		       created_at, COALESCE(published_at, '')
		FROM guardrail_versions
		WHERE guardrail_id=?
		ORDER BY version_number ASC`, guardrailID)
	if err != nil {
		return nil, fmt.Errorf("store: list guardrail version snapshots for %s: %w", guardrailID, err)
	}
	defer rows.Close()
	var versions []fleet.CatalogVersion
	for rows.Next() {
		var version fleet.CatalogVersion
		var enabled int
		if err := rows.Scan(
			&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
			&enabled, &version.Position, &version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
			&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan guardrail version snapshot: %w", err)
		}
		version.Enabled = enabled != 0
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func listCatalogVersions(q querier, table, column, assetID string) ([]fleet.CatalogVersion, error) {
	rows, err := q.Query(`
		SELECT id, `+column+`, version_number, state, source_type, source_ref, author, changelog,
		       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
		FROM `+table+`
		WHERE `+column+`=?
		ORDER BY version_number DESC`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: list %s for %s: %w", table, assetID, err)
	}
	defer rows.Close()
	var versions []fleet.CatalogVersion
	for rows.Next() {
		var version fleet.CatalogVersion
		if err := rows.Scan(
			&version.ID, &version.AssetID, &version.Version, &version.State, &version.SourceType, &version.SourceRef,
			&version.Author, &version.Changelog, &version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan %s: %w", table, err)
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func replacePromptVersionSnapshotsTx(tx *sql.Tx, promptID string, versions []fleet.CatalogVersion) (fleet.CatalogVersion, error) {
	if len(versions) == 0 {
		return fleet.CatalogVersion{}, nil
	}
	if _, err := tx.Exec("DELETE FROM prompt_versions WHERE prompt_id=?", promptID); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: replace prompt versions: %w", err)
	}
	var current fleet.CatalogVersion
	for _, version := range orderedCatalogVersionSnapshots(versions) {
		if err := normalizeCatalogVersionSnapshot(&version, "promptver_", catalogBodyHash(version.Description, version.Content)); err != nil {
			return fleet.CatalogVersion{}, err
		}
		if version.Content == "" {
			return fleet.CatalogVersion{}, &ErrValidation{Msg: "prompt version content is required"}
		}
		if _, err := tx.Exec(`
			INSERT INTO prompt_versions
				(id, prompt_id, version_number, state, description, content, source_type, source_ref, author, changelog,
				 base_version_id, body_hash, created_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, COALESCE(NULLIF(?, ''), datetime('now')), NULLIF(?, ''))`,
			version.ID, promptID, version.Version, version.State, version.Description, version.Content, version.SourceType,
			version.SourceRef, version.Author, version.Changelog, version.BaseVersionID, version.BodyHash, version.CreatedAt,
			version.PublishedAt,
		); err != nil {
			return fleet.CatalogVersion{}, fmt.Errorf("store: insert prompt version %d: %w", version.Version, err)
		}
		if version.State == "published" && version.Version >= current.Version {
			current = version
		}
	}
	if current.ID == "" {
		return fleet.CatalogVersion{}, &ErrValidation{Msg: "prompt versions require at least one published version"}
	}
	if _, err := tx.Exec("UPDATE prompts SET description=?, content=?, current_version_id=?, updated_at=datetime('now') WHERE id=?", current.Description, current.Content, current.ID, promptID); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: update prompt current version: %w", err)
	}
	return current, nil
}

func replaceSkillVersionSnapshotsTx(tx *sql.Tx, skillID string, versions []fleet.CatalogVersion) (fleet.CatalogVersion, error) {
	if len(versions) == 0 {
		return fleet.CatalogVersion{}, nil
	}
	if _, err := tx.Exec("DELETE FROM skill_versions WHERE skill_id=?", skillID); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: replace skill versions: %w", err)
	}
	var current fleet.CatalogVersion
	for _, version := range orderedCatalogVersionSnapshots(versions) {
		if err := normalizeCatalogVersionSnapshot(&version, "skillver_", catalogBodyHash(version.Prompt)); err != nil {
			return fleet.CatalogVersion{}, err
		}
		if version.Prompt == "" {
			return fleet.CatalogVersion{}, &ErrValidation{Msg: "skill version prompt is required"}
		}
		if _, err := tx.Exec(`
			INSERT INTO skill_versions
				(id, skill_id, version_number, state, prompt, source_type, source_ref, author, changelog,
				 base_version_id, body_hash, created_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, COALESCE(NULLIF(?, ''), datetime('now')), NULLIF(?, ''))`,
			version.ID, skillID, version.Version, version.State, version.Prompt, version.SourceType, version.SourceRef,
			version.Author, version.Changelog, version.BaseVersionID, version.BodyHash, version.CreatedAt, version.PublishedAt,
		); err != nil {
			return fleet.CatalogVersion{}, fmt.Errorf("store: insert skill version %d: %w", version.Version, err)
		}
		if version.State == "published" && version.Version >= current.Version {
			current = version
		}
	}
	if current.ID == "" {
		return fleet.CatalogVersion{}, &ErrValidation{Msg: "skill versions require at least one published version"}
	}
	if _, err := tx.Exec("UPDATE skills SET prompt=?, current_version_id=? WHERE id=?", current.Prompt, current.ID, skillID); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: update skill current version: %w", err)
	}
	return current, nil
}

func replaceGuardrailVersionSnapshotsTx(tx *sql.Tx, guardrailID string, versions []fleet.CatalogVersion) (fleet.CatalogVersion, error) {
	if len(versions) == 0 {
		return fleet.CatalogVersion{}, nil
	}
	if _, err := tx.Exec("DELETE FROM guardrail_versions WHERE guardrail_id=?", guardrailID); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: replace guardrail versions: %w", err)
	}
	var current fleet.CatalogVersion
	for _, version := range orderedCatalogVersionSnapshots(versions) {
		hash := catalogBodyHash(version.Description, version.Content, fmt.Sprint(version.Enabled), fmt.Sprint(version.Position))
		if err := normalizeCatalogVersionSnapshot(&version, "guardrailver_", hash); err != nil {
			return fleet.CatalogVersion{}, err
		}
		if version.Content == "" {
			return fleet.CatalogVersion{}, &ErrValidation{Msg: "guardrail version content is required"}
		}
		if _, err := tx.Exec(`
			INSERT INTO guardrail_versions
				(id, guardrail_id, version_number, state, description, content, enabled, position, source_type, source_ref,
				 author, changelog, base_version_id, body_hash, created_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, COALESCE(NULLIF(?, ''), datetime('now')), NULLIF(?, ''))`,
			version.ID, guardrailID, version.Version, version.State, version.Description, version.Content,
			boolToInt(version.Enabled), version.Position, version.SourceType, version.SourceRef, version.Author,
			version.Changelog, version.BaseVersionID, version.BodyHash, version.CreatedAt, version.PublishedAt,
		); err != nil {
			return fleet.CatalogVersion{}, fmt.Errorf("store: insert guardrail version %d: %w", version.Version, err)
		}
		if version.State == "published" && version.Version >= current.Version {
			current = version
		}
	}
	if current.ID == "" {
		return fleet.CatalogVersion{}, &ErrValidation{Msg: "guardrail versions require at least one published version"}
	}
	if _, err := tx.Exec(`
		UPDATE guardrails
		SET description=?, content=?, enabled=?, position=?, current_version_id=?, updated_at=datetime('now')
		WHERE id=?`,
		current.Description, current.Content, boolToInt(current.Enabled), current.Position, current.ID, guardrailID,
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: update guardrail current version: %w", err)
	}
	return current, nil
}

func orderedCatalogVersionSnapshots(versions []fleet.CatalogVersion) []fleet.CatalogVersion {
	ordered := slices.Clone(versions)
	slices.SortFunc(ordered, func(a, b fleet.CatalogVersion) int {
		return a.Version - b.Version
	})
	return ordered
}

func normalizeCatalogVersionSnapshot(version *fleet.CatalogVersion, prefix, hash string) error {
	if version.Version <= 0 {
		return &ErrValidation{Msg: "catalog version number must be positive"}
	}
	switch version.State {
	case "draft", "proposal", "published":
	default:
		return &ErrValidation{Msg: fmt.Sprintf("invalid catalog version state %q", version.State)}
	}
	if strings.TrimSpace(version.ID) == "" {
		id, err := newCatalogInternalID(prefix)
		if err != nil {
			return err
		}
		version.ID = id
	}
	if version.SourceType == "" {
		version.SourceType = "manual"
	}
	if version.BodyHash == "" {
		version.BodyHash = hash
	}
	return nil
}

func CreateSkillDraftTx(tx *sql.Tx, ref, prompt string) (fleet.CatalogVersion, error) {
	skillID, err := skillInternalID(tx, ref)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	version, err := createSkillVersionTx(tx, skillID, "draft", prompt)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	return version, nil
}

func CreateGuardrailDraftTx(tx *sql.Tx, ref string, g fleet.Guardrail) (fleet.CatalogVersion, error) {
	guardrailID, err := guardrailInternalID(tx, ref)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	version, err := createGuardrailVersionTx(tx, guardrailID, "draft", g)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	return version, nil
}

func createPromptVersionTx(tx *sql.Tx, promptID, state, description, content string) (fleet.CatalogVersion, error) {
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
	hash := catalogBodyHash(description, content)
	var publishedAt any
	if state == "published" {
		publishedAt = "now"
	}
	if _, err := tx.Exec(`
		INSERT INTO prompt_versions
			(id, prompt_id, version_number, state, description, content, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, 'manual', NULLIF(?, ''), ?, datetime('now'), CASE WHEN ?='now' THEN datetime('now') ELSE NULL END)`,
		id, promptID, version, state, description, content, baseID, hash, publishedAt,
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: create prompt version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: promptID, Version: version, State: state, SourceType: "manual", BaseVersionID: baseID, BodyHash: hash}, nil
}

func createSkillVersionTx(tx *sql.Tx, skillID, state, prompt string) (fleet.CatalogVersion, error) {
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
	hash := catalogBodyHash(prompt)
	var publishedAt any
	if state == "published" {
		publishedAt = "now"
	}
	if _, err := tx.Exec(`
		INSERT INTO skill_versions
			(id, skill_id, version_number, state, prompt, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, ?, ?, 'manual', NULLIF(?, ''), ?, datetime('now'), CASE WHEN ?='now' THEN datetime('now') ELSE NULL END)`,
		id, skillID, version, state, prompt, baseID, hash, publishedAt,
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: create skill version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: skillID, Version: version, State: state, SourceType: "manual", BaseVersionID: baseID, BodyHash: hash}, nil
}

func createGuardrailVersionTx(tx *sql.Tx, guardrailID, state string, g fleet.Guardrail) (fleet.CatalogVersion, error) {
	version, err := nextCatalogVersion(tx, "guardrail_versions", "guardrail_id", guardrailID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	id, err := newCatalogInternalID("guardrailver_")
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	baseID, err := currentVersionID(tx, "guardrails", guardrailID)
	if err != nil {
		return fleet.CatalogVersion{}, err
	}
	hash := catalogBodyHash(g.Description, g.Content, fmt.Sprint(g.Enabled), fmt.Sprint(g.Position))
	var publishedAt any
	if state == "published" {
		publishedAt = "now"
	}
	if _, err := tx.Exec(`
		INSERT INTO guardrail_versions
			(id, guardrail_id, version_number, state, description, content, enabled, position, source_type, base_version_id, body_hash, created_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'manual', NULLIF(?, ''), ?, datetime('now'), CASE WHEN ?='now' THEN datetime('now') ELSE NULL END)`,
		id, guardrailID, version, state, g.Description, g.Content, boolToInt(g.Enabled), g.Position, baseID, hash, publishedAt,
	); err != nil {
		return fleet.CatalogVersion{}, fmt.Errorf("store: create guardrail version: %w", err)
	}
	return fleet.CatalogVersion{ID: id, AssetID: guardrailID, Version: version, State: state, SourceType: "manual", BaseVersionID: baseID, BodyHash: hash}, nil
}

func PublishPromptVersionTx(tx *sql.Tx, versionID string) (fleet.Prompt, error) {
	versionID = strings.TrimSpace(versionID)
	var p fleet.Prompt
	var promptID, state string
	var baseVersionID, currentVersionID sql.NullString
	err := tx.QueryRow(`
		SELECT p.id, p.ref, COALESCE(p.workspace_id, ''), COALESCE(p.repo, ''), p.name,
		       pv.description, pv.content, pv.state, pv.version_number, pv.base_version_id, p.current_version_id
		FROM prompt_versions pv
		JOIN prompts p ON p.id = pv.prompt_id
		WHERE pv.id=?`, versionID).
		Scan(&promptID, &p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content, &state, &p.Version, &baseVersionID, &currentVersionID)
	if err != nil {
		return fleet.Prompt{}, versionReadErr("prompt", versionID, err)
	}
	if state != "draft" && state != "proposal" {
		return fleet.Prompt{}, &ErrValidation{Msg: "only draft or proposal prompt versions can be published"}
	}
	if staleCatalogDraft(baseVersionID, currentVersionID) {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt version is stale; refresh from the current published version before publishing"}
	}
	if _, err := tx.Exec("UPDATE prompt_versions SET state='published', published_at=datetime('now') WHERE id=?", versionID); err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: publish prompt version %s: %w", versionID, err)
	}
	if _, err := tx.Exec("UPDATE prompts SET description=?, content=?, current_version_id=?, updated_at=datetime('now') WHERE id=?", p.Description, p.Content, versionID, promptID); err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: publish prompt version %s current: %w", versionID, err)
	}
	p.VersionID = versionID
	return p, nil
}

func PublishSkillVersionTx(tx *sql.Tx, versionID string) (string, fleet.Skill, error) {
	versionID = strings.TrimSpace(versionID)
	var sk fleet.Skill
	var skillID, state, ref string
	var baseVersionID, currentVersionID sql.NullString
	err := tx.QueryRow(`
		SELECT s.id, s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name,
		       sv.prompt, sv.state, sv.version_number, sv.base_version_id, s.current_version_id
		FROM skill_versions sv
		JOIN skills s ON s.id = sv.skill_id
		WHERE sv.id=?`, versionID).
		Scan(&skillID, &ref, &sk.WorkspaceID, &sk.Repo, &sk.Name, &sk.Prompt, &state, &sk.Version, &baseVersionID, &currentVersionID)
	if err != nil {
		return "", fleet.Skill{}, versionReadErr("skill", versionID, err)
	}
	if state != "draft" && state != "proposal" {
		return "", fleet.Skill{}, &ErrValidation{Msg: "only draft or proposal skill versions can be published"}
	}
	if staleCatalogDraft(baseVersionID, currentVersionID) {
		return "", fleet.Skill{}, &ErrValidation{Msg: "skill version is stale; refresh from the current published version before publishing"}
	}
	if _, err := tx.Exec("UPDATE skill_versions SET state='published', published_at=datetime('now') WHERE id=?", versionID); err != nil {
		return "", fleet.Skill{}, fmt.Errorf("store: publish skill version %s: %w", versionID, err)
	}
	if _, err := tx.Exec("UPDATE skills SET prompt=?, current_version_id=? WHERE id=?", sk.Prompt, versionID, skillID); err != nil {
		return "", fleet.Skill{}, fmt.Errorf("store: publish skill version %s current: %w", versionID, err)
	}
	sk.ID = ref
	sk.VersionID = versionID
	return ref, sk, nil
}

func PublishGuardrailVersionTx(tx *sql.Tx, versionID string) (fleet.Guardrail, error) {
	versionID = strings.TrimSpace(versionID)
	var g fleet.Guardrail
	var guardrailID, state string
	var enabled int
	var baseVersionID, currentVersionID sql.NullString
	err := tx.QueryRow(`
		SELECT g.id, COALESCE(g.workspace_id, ''), g.name, gv.description, gv.content, gv.enabled, gv.position,
		       gv.state, gv.version_number, gv.base_version_id, g.current_version_id
		FROM guardrail_versions gv
		JOIN guardrails g ON g.id = gv.guardrail_id
		WHERE gv.id=?`, versionID).
		Scan(&guardrailID, &g.WorkspaceID, &g.Name, &g.Description, &g.Content, &enabled, &g.Position, &state, &g.Version, &baseVersionID, &currentVersionID)
	if err != nil {
		return fleet.Guardrail{}, versionReadErr("guardrail", versionID, err)
	}
	if state != "draft" && state != "proposal" {
		return fleet.Guardrail{}, &ErrValidation{Msg: "only draft or proposal guardrail versions can be published"}
	}
	if staleCatalogDraft(baseVersionID, currentVersionID) {
		return fleet.Guardrail{}, &ErrValidation{Msg: "guardrail version is stale; refresh from the current published version before publishing"}
	}
	if _, err := tx.Exec("UPDATE guardrail_versions SET state='published', published_at=datetime('now') WHERE id=?", versionID); err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: publish guardrail version %s: %w", versionID, err)
	}
	if _, err := tx.Exec(`
		UPDATE guardrails
		SET description=?, content=?, enabled=?, position=?, current_version_id=?, updated_at=datetime('now')
		WHERE id=?`, g.Description, g.Content, enabled, g.Position, versionID, guardrailID); err != nil {
		return fleet.Guardrail{}, fmt.Errorf("store: publish guardrail version %s current: %w", versionID, err)
	}
	g.ID = g.Name
	g.Enabled = enabled != 0
	g.VersionID = versionID
	return g, nil
}

func staleCatalogDraft(baseVersionID, currentVersionID sql.NullString) bool {
	return baseVersionID.Valid && currentVersionID.Valid && baseVersionID.String != currentVersionID.String
}

func promptInternalID(q querier, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var id string
	if err := q.QueryRow("SELECT id FROM prompts WHERE id=? OR ref=?", ref, ref).Scan(&id); err != nil {
		return "", catalogReadErr("prompt", ref, err)
	}
	return id, nil
}

func skillInternalID(q querier, ref string) (string, error) {
	ref = fleet.NormalizeSkillName(ref)
	var id string
	if err := q.QueryRow("SELECT id FROM skills WHERE id=? OR ref=? OR name=?", ref, ref, ref).Scan(&id); err != nil {
		return "", catalogReadErr("skill", ref, err)
	}
	return id, nil
}

func guardrailInternalID(q querier, ref string) (string, error) {
	ref = fleet.NormalizeGuardrailName(ref)
	var id string
	if err := q.QueryRow("SELECT id FROM guardrails WHERE id=? OR ref=? OR name=?", ref, ref, ref).Scan(&id); err != nil {
		return "", catalogReadErr("guardrail", ref, err)
	}
	return id, nil
}

func catalogReadErr(kind, ref string, err error) error {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return &ErrNotFound{Msg: fmt.Sprintf("%s %q not found", kind, ref)}
	}
	return fmt.Errorf("store: read %s %s: %w", kind, ref, err)
}

func versionReadErr(kind, versionID string, err error) error {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return &ErrNotFound{Msg: fmt.Sprintf("%s version %q not found", kind, versionID)}
	}
	return fmt.Errorf("store: read %s version %s: %w", kind, versionID, err)
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
