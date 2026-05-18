package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
		if id == "" || s.Name == "" {
			return fmt.Errorf("store import: skill requires id and name")
		}
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: skill %q: %w", id, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO skills (id, workspace_id, repo, name, prompt)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				prompt = excluded.prompt`,
			id, s.WorkspaceID, s.Repo, s.Name, s.Prompt,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: skill name %q is already used by another skill in that scope", s.Name)
			}
			return fmt.Errorf("store import: upsert skill %s: %w", id, err)
		}
	}
	return nil
}

func loadSkills(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT id,COALESCE(workspace_id, ''),COALESCE(repo, ''),name,prompt FROM skills")
	if err != nil {
		return fmt.Errorf("store load: query skills: %w", err)
	}
	defer rows.Close()

	skills := make(map[string]fleet.Skill)
	for rows.Next() {
		var id string
		var skill fleet.Skill
		if err := rows.Scan(&id, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt); err != nil {
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

func readSkillScopeByID(q querier, id string) (fleet.Skill, bool, error) {
	var skill fleet.Skill
	err := q.QueryRow("SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name FROM skills WHERE id = ?", id).
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
	if name == "" {
		var existingID string
		err := queryCatalogIDByScopeName(tx, "skills", s.WorkspaceID, s.Repo, s.Name).Scan(&existingID)
		if err == nil {
			name = existingID
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
	refs, err := agentsReferencingSkill(tx, name)
	if err != nil {
		return fmt.Errorf("store: delete skill %s: check agents: %w", name, err)
	}
	if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("skill %q is referenced by %d agent(s): %s", name, len(refs), formatReferenceList(refs))}
	}
	if _, err := tx.Exec("DELETE FROM skills WHERE id=?", name); err != nil {
		return fmt.Errorf("store: delete skill %s: %w", name, err)
	}
	return nil
}

func agentsReferencingSkill(q querier, skillID string) ([]string, error) {
	rows, err := q.Query("SELECT workspace_id, name, skills FROM agents ORDER BY workspace_id, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var workspaceID, name, skillsJSON string
		if err := rows.Scan(&workspaceID, &name, &skillsJSON); err != nil {
			return nil, err
		}
		var skills []string
		if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
			return nil, fmt.Errorf("parse agent %s skills: %w", name, err)
		}
		for _, skill := range skills {
			if fleet.NormalizeSkillName(skill) == skillID {
				refs = append(refs, workspaceAgentRef(workspaceID, name))
				break
			}
		}
	}
	return refs, rows.Err()
}
