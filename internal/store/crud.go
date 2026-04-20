package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/config"
)

// ──── Agents ─────────────────────────────────────────────────────────────────

// ReadAgents returns all agents from the database, ordered by name.
func ReadAgents(db *sql.DB) ([]config.AgentDef, error) {
	var cfg config.Config
	if err := loadAgents(db, &cfg); err != nil {
		return nil, err
	}
	return cfg.Agents, nil
}

// UpsertAgent inserts or replaces a single agent definition.
func UpsertAgent(db *sql.DB, a config.AgentDef) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert agent %s: begin: %w", a.Name, err)
	}
	defer tx.Rollback()
	if err := importAgents(tx, []config.AgentDef{a}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAgent removes the agent with the given name. It is not an error to
// delete a name that does not exist.
func DeleteAgent(db *sql.DB, name string) error {
	if _, err := db.Exec("DELETE FROM agents WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	return nil
}

// ──── Skills ─────────────────────────────────────────────────────────────────

// ReadSkills returns all skills from the database.
func ReadSkills(db *sql.DB) (map[string]config.SkillDef, error) {
	var cfg config.Config
	if err := loadSkills(db, &cfg); err != nil {
		return nil, err
	}
	if cfg.Skills == nil {
		return map[string]config.SkillDef{}, nil
	}
	return cfg.Skills, nil
}

// UpsertSkill inserts or replaces a single skill.
func UpsertSkill(db *sql.DB, name string, s config.SkillDef) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importSkills(tx, map[string]config.SkillDef{name: s}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteSkill removes the skill with the given name.
func DeleteSkill(db *sql.DB, name string) error {
	if _, err := db.Exec("DELETE FROM skills WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete skill %s: %w", name, err)
	}
	return nil
}

// ──── Backends ───────────────────────────────────────────────────────────────

// ReadBackends returns all AI backend configurations from the database.
func ReadBackends(db *sql.DB) (map[string]config.AIBackendConfig, error) {
	var cfg config.Config
	if err := loadBackends(db, &cfg); err != nil {
		return nil, err
	}
	if cfg.Daemon.AIBackends == nil {
		return map[string]config.AIBackendConfig{}, nil
	}
	return cfg.Daemon.AIBackends, nil
}

// UpsertBackend inserts or replaces a single AI backend configuration.
func UpsertBackend(db *sql.DB, name string, b config.AIBackendConfig) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importBackends(tx, map[string]config.AIBackendConfig{name: b}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteBackend removes the backend with the given name.
func DeleteBackend(db *sql.DB, name string) error {
	if _, err := db.Exec("DELETE FROM backends WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete backend %s: %w", name, err)
	}
	return nil
}

// ──── Repos ──────────────────────────────────────────────────────────────────

// ReadRepos returns all repos (with bindings) from the database.
func ReadRepos(db *sql.DB) ([]config.RepoDef, error) {
	var cfg config.Config
	if err := loadRepos(db, &cfg); err != nil {
		return nil, err
	}
	return cfg.Repos, nil
}

// UpsertRepo inserts or replaces a repo and its bindings. Bindings are
// replaced wholesale: any existing bindings for the repo are removed before
// the new list is written.
func UpsertRepo(db *sql.DB, r config.RepoDef) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert repo %s: begin: %w", r.Name, err)
	}
	defer tx.Rollback()
	if err := importRepos(tx, []config.RepoDef{r}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRepo removes a repo and all of its bindings.
func DeleteRepo(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete repo %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM bindings WHERE repo=?", name); err != nil {
		return fmt.Errorf("store: delete bindings for repo %s: %w", name, err)
	}
	if _, err := tx.Exec("DELETE FROM repos WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete repo %s: %w", name, err)
	}
	return tx.Commit()
}
