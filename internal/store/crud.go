package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/config"
)

// validateCrossRefs reads all mutable entity tables through q (a *sql.Tx in
// practice, so reads see the pending transaction state) and verifies that the
// post-mutation snapshot is free of dangling cross-entity references. If q is
// a *sql.Tx and validation fails, the caller must roll back the transaction to
// prevent the invalid state from reaching SQLite.
func validateCrossRefs(q querier) error {
	var cfg config.Config
	if err := loadBackends(q, &cfg); err != nil {
		return err
	}
	if err := loadSkills(q, &cfg); err != nil {
		return err
	}
	if err := loadAgents(q, &cfg); err != nil {
		return err
	}
	if err := loadRepos(q, &cfg); err != nil {
		return err
	}
	backends := cfg.Daemon.AIBackends
	if backends == nil {
		backends = map[string]config.AIBackendConfig{}
	}
	skills := cfg.Skills
	if skills == nil {
		skills = map[string]config.SkillDef{}
	}
	return config.ValidateCrossRefs(cfg.Agents, cfg.Repos, skills, backends)
}

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
	if err := validateCrossRefs(tx); err != nil {
		return fmt.Errorf("store: upsert agent %s: %w", a.Name, err)
	}
	return tx.Commit()
}

// DeleteAgent removes the agent with the given name. It is not an error to
// delete a name that does not exist. Returns an error if the agent is still
// referenced by any repo binding or can_dispatch list.
func DeleteAgent(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete agent %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM agents WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	if err := validateCrossRefs(tx); err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	return tx.Commit()
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

// DeleteSkill removes the skill with the given name. Returns an error if any
// agent still references the skill.
func DeleteSkill(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM skills WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete skill %s: %w", name, err)
	}
	if err := validateCrossRefs(tx); err != nil {
		return fmt.Errorf("store: delete skill %s: %w", name, err)
	}
	return tx.Commit()
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

// DeleteBackend removes the backend with the given name. Returns an error if
// any agent still references the backend.
func DeleteBackend(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM backends WHERE name=?", name); err != nil {
		return fmt.Errorf("store: delete backend %s: %w", name, err)
	}
	if err := validateCrossRefs(tx); err != nil {
		return fmt.Errorf("store: delete backend %s: %w", name, err)
	}
	return tx.Commit()
}

// ReadSnapshot returns agents, repos, skills, and backends as a consistent
// point-in-time snapshot by reading all four within a single SQLite transaction.
// This prevents the race where a concurrent /api/store write commits between
// reads, producing a mixed snapshot that can cause spurious Reload failures or
// agents that see new definitions with stale skill/backend maps.
func ReadSnapshot(db *sql.DB) ([]config.AgentDef, []config.RepoDef, map[string]config.SkillDef, map[string]config.AIBackendConfig, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("store: begin snapshot: %w", err)
	}
	defer tx.Rollback()

	var cfg config.Config
	if err := loadAgents(tx, &cfg); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := loadRepos(tx, &cfg); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := loadSkills(tx, &cfg); err != nil {
		return nil, nil, nil, nil, err
	}
	if err := loadBackends(tx, &cfg); err != nil {
		return nil, nil, nil, nil, err
	}
	if cfg.Skills == nil {
		cfg.Skills = map[string]config.SkillDef{}
	}
	if cfg.Daemon.AIBackends == nil {
		cfg.Daemon.AIBackends = map[string]config.AIBackendConfig{}
	}
	return cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends, nil
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
	if err := validateCrossRefs(tx); err != nil {
		return fmt.Errorf("store: upsert repo %s: %w", r.Name, err)
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
