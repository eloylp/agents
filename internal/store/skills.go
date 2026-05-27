package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importSkills(tx *sql.Tx, skills map[string]fleet.Skill) error {
	for id, s := range skills {
		id = fleet.NormalizeSkillName(id)
		fleet.NormalizeSkill(&s)
		if s.Name == "" {
			s.Name = id
		}
		if s.WorkspaceID == "" && s.Repo != "" {
			return fmt.Errorf("store import: skill %q repo scope requires workspace_id", id)
		}
		if err := ensureCatalogScope(tx, "skill", s.WorkspaceID, s.Repo); err != nil {
			return err
		}
		if id == "" || s.Name == "" {
			return fmt.Errorf("store import: skill requires id and name")
		}
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: skill %q: %w", id, err)
		}
		internalID, _, err := resolveCatalogID(tx, "skills", id)
		if errors.Is(err, sql.ErrNoRows) {
			internalID, err = newCatalogInternalID("skill_")
		}
		if err != nil {
			return fmt.Errorf("store import: skill %q: resolve id: %w", id, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO skills (id, ref, workspace_id, repo, name, prompt)
			VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
			ON CONFLICT(ref) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				prompt = excluded.prompt`,
			internalID, id, s.WorkspaceID, s.Repo, s.Name, s.Prompt,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: skill name %q is already used by another skill in that scope", s.Name)
			}
			return fmt.Errorf("store import: upsert skill %s: %w", id, err)
		}
		version, err := publishSkillVersionTx(tx, internalID, s.Prompt)
		if err != nil {
			return fmt.Errorf("store import: publish skill %s version: %w", id, err)
		}
		if len(s.Versions) > 0 {
			version, err = replaceSkillVersionSnapshotsTx(tx, internalID, s.Versions)
			if err != nil {
				return fmt.Errorf("store import: replace skill %s versions: %w", id, err)
			}
		}
		if _, err := tx.Exec("UPDATE skills SET current_version_id=? WHERE id=?", version.ID, internalID); err != nil {
			return fmt.Errorf("store import: update skill %s current version: %w", id, err)
		}
	}
	return nil
}

func loadSkills(db querier, cfg *config.Config) error {
	rows, err := db.Query(`
		SELECT s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name, s.prompt,
		       COALESCE(sv.id, ''), COALESCE(sv.version_number, 0)
		FROM skills s
		LEFT JOIN skill_versions sv ON sv.id = s.current_version_id`)
	if err != nil {
		return fmt.Errorf("store load: query skills: %w", err)
	}
	defer rows.Close()

	skills := make(map[string]fleet.Skill)
	for rows.Next() {
		var id string
		var skill fleet.Skill
		if err := rows.Scan(&id, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt, &skill.VersionID, &skill.Version); err != nil {
			return fmt.Errorf("store load: scan skill: %w", err)
		}
		skill.ID = id
		skills[id] = skill
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate skills: %w", err)
	}
	cfg.Skills = skills
	return nil
}

func ReadSkillVersion(db *sql.DB, versionID string) (fleet.Skill, error) {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return fleet.Skill{}, &ErrValidation{Msg: "skill version id is required"}
	}
	var skill fleet.Skill
	row := db.QueryRow(`
		SELECT s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name,
		       sv.prompt, sv.id, sv.version_number
		FROM skill_versions sv
		JOIN skills s ON s.id = sv.skill_id
		WHERE sv.id = ? AND sv.state = 'published'`, versionID)
	err := row.Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt, &skill.VersionID, &skill.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, &ErrNotFound{Msg: fmt.Sprintf("skill version %q not found", versionID)}
	}
	if err != nil {
		return fleet.Skill{}, fmt.Errorf("store: read skill version %s: %w", versionID, err)
	}
	return skill, nil
}

func readSkillScopeByID(q querier, id string) (fleet.Skill, bool, error) {
	var skill fleet.Skill
	err := q.QueryRow("SELECT ref, COALESCE(workspace_id, ''), COALESCE(repo, ''), name FROM skills WHERE id = ? OR ref = ?", id, id).
		Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name)
	if err == nil {
		return skill, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, false, nil
	}
	return fleet.Skill{}, false, err
}

// ──── Skills ─────────────────────────────────────────────────────────────────────────────────────

// ReadSkills returns all skills from the database.
func ReadSkills(db *sql.DB) (map[string]fleet.Skill, error) {
	var cfg config.Config
	if err := loadSkills(db, &cfg); err != nil {
		return nil, err
	}
	if cfg.Skills == nil {
		return map[string]fleet.Skill{}, nil
	}
	return cfg.Skills, nil
}

// UpsertSkill inserts or replaces a single skill.
// The skill name is normalized (lowercase, trimmed) and Skill.Prompt is
// trimmed before writing, matching the normalization startup applies in
// normalize() so that the stored values are already in canonical form and
// validation sees the same shape as after a restart.
func UpsertSkill(db *sql.DB, name string, s fleet.Skill) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := UpsertSkillTx(tx, name, s); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert skill %s: %v", name, err)}
	}
	return tx.Commit()
}

func UpsertSkillTx(tx *sql.Tx, name string, s fleet.Skill) error {
	name = fleet.NormalizeSkillName(name)
	fleet.NormalizeSkill(&s)
	if s.Name == "" {
		s.Name = name
	}
	if s.WorkspaceID == "" && s.Repo != "" {
		return &ErrValidation{Msg: fmt.Sprintf("store: skill %q repo scope requires workspace_id", name)}
	}
	if err := ensureCatalogScope(tx, "skill", s.WorkspaceID, s.Repo); err != nil {
		return err
	}
	if name == "" {
		var existingID, existingRef string
		err := queryCatalogRefByScopeName(tx, "skills", s.WorkspaceID, s.Repo, s.Name).Scan(&existingID, &existingRef)
		if err == nil {
			name = existingRef
		} else if errors.Is(err, sql.ErrNoRows) {
			id, derr := derivedCatalogID("skill_", s.WorkspaceID, s.Repo, s.Name)
			if derr != nil {
				return &ErrValidation{Msg: fmt.Sprintf("store: skill %q: %v", s.Name, derr)}
			}
			name = id
		} else {
			return fmt.Errorf("store: upsert skill %q: read existing: %w", s.Name, err)
		}
	}
	if name == "" || s.Name == "" {
		return &ErrValidation{Msg: "store: skill requires id and name"}
	}
	if err := validateEntityID(name); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: skill %q: %v", s.Name, err)}
	}
	if err := importSkills(tx, map[string]fleet.Skill{name: s}); err != nil {
		return err
	}
	return nil
}

// DeleteSkill removes the skill with the given name. Returns an error if any
// agent still references the skill.
func DeleteSkill(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := DeleteSkillTx(tx, name); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete skill %s: commit: %w", name, err)
	}
	return nil
}

func DeleteSkillTx(tx *sql.Tx, name string) error {
	name = fleet.NormalizeSkillName(name)
	id, _, err := resolveCatalogID(tx, "skills", name)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrNotFound{Msg: fmt.Sprintf("skill %q not found", name)}
	}
	if err != nil {
		return fmt.Errorf("store: delete skill %s: lookup: %w", name, err)
	}
	refs, err := agentsReferencingSkill(tx, id)
	if err != nil {
		return fmt.Errorf("store: delete skill %s: check agents: %w", name, err)
	}
	if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("skill %q is referenced by %d agent(s): %s", name, len(refs), formatReferenceList(refs))}
	}
	if _, err := tx.Exec("DELETE FROM skills WHERE id=?", id); err != nil {
		return fmt.Errorf("store: delete skill %s: %w", name, err)
	}
	return nil
}

func agentsReferencingSkill(q querier, skillID string) ([]string, error) {
	rows, err := q.Query(`
		SELECT a.workspace_id, a.name
		FROM agent_skills ask
		JOIN agents a ON a.id = ask.agent_id
		WHERE ask.skill_id=?
		ORDER BY a.workspace_id, a.name`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var workspaceID, name string
		if err := rows.Scan(&workspaceID, &name); err != nil {
			return nil, err
		}
		refs = append(refs, workspaceAgentRef(workspaceID, name))
	}
	return refs, rows.Err()
}
