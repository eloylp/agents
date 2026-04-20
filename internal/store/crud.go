package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/eloylp/agents/internal/config"
)

// validateFleet reads all four mutable entity tables through q (a *sql.Tx in
// practice, so reads see the pending transaction state) and verifies that the
// post-mutation snapshot satisfies both field-level constraints and cross-entity
// reference consistency via config.ValidateEntities. Aggregate minimums ("at
// least one agent/repo/backend required") are NOT checked here; DELETE paths
// enforce those separately with requireAtLeastOne* helpers below.
func validateFleet(q querier) error {
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
	return config.ValidateEntities(cfg.Agents, cfg.Repos, skills, backends)
}

// requireAtLeastOneAgent returns an error if the transaction would leave the
// agents table empty — used by DeleteAgent to enforce the "at least one agent"
// invariant without running a full validateFleet.
func requireAtLeastOneAgent(q querier) error {
	var n int
	if err := q.QueryRow("SELECT COUNT(*) FROM agents").Scan(&n); err != nil {
		return fmt.Errorf("store: count agents: %w", err)
	}
	if n == 0 {
		return errors.New("config: at least one agent is required")
	}
	return nil
}

// requireAtLeastOneBackend returns an error if the transaction would leave the
// backends table empty.
func requireAtLeastOneBackend(q querier) error {
	var n int
	if err := q.QueryRow("SELECT COUNT(*) FROM backends").Scan(&n); err != nil {
		return fmt.Errorf("store: count backends: %w", err)
	}
	if n == 0 {
		return errors.New("config: at least one ai_backends entry is required")
	}
	return nil
}

// requireAtLeastOneEnabledRepo returns an error if the transaction would leave
// no enabled repos — used by DeleteRepo.
func requireAtLeastOneEnabledRepo(q querier) error {
	var n int
	if err := q.QueryRow("SELECT COUNT(*) FROM repos WHERE enabled=1").Scan(&n); err != nil {
		return fmt.Errorf("store: count enabled repos: %w", err)
	}
	if n == 0 {
		return errors.New("config: at least one repo must be enabled")
	}
	return nil
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
	if err := validateFleet(tx); err != nil {
		return fmt.Errorf("store: upsert agent %s: %w", a.Name, err)
	}
	return tx.Commit()
}

// DeleteAgent removes the agent with the given name. It is not an error to
// delete a name that does not exist. Returns an error if the agent is still
// referenced by any repo binding or can_dispatch list, or if it is the last agent.
func DeleteAgent(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete agent %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM agents WHERE name=?", name)
	if err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := requireAtLeastOneAgent(tx); err != nil {
			return fmt.Errorf("store: delete agent %s: %w", name, err)
		}
		if err := validateFleet(tx); err != nil {
			return fmt.Errorf("store: delete agent %s: %w", name, err)
		}
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
	if err := validateFleet(tx); err != nil {
		return fmt.Errorf("store: upsert skill %s: %w", name, err)
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
	if err := validateFleet(tx); err != nil {
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
// Zero-value numeric fields are normalised to startup defaults before
// persistence so that the stored config matches what FinishLoad would
// produce on restart (e.g. timeout_seconds 0 → 600, max_prompt_chars 0 → 12000).
func UpsertBackend(db *sql.DB, name string, b config.AIBackendConfig) error {
	config.ApplyBackendDefaults(&b)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importBackends(tx, map[string]config.AIBackendConfig{name: b}); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return fmt.Errorf("store: upsert backend %s: %w", name, err)
	}
	return tx.Commit()
}

// DeleteBackend removes the backend with the given name. Returns an error if
// any agent still references the backend, or if it is the last backend.
func DeleteBackend(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM backends WHERE name=?", name)
	if err != nil {
		return fmt.Errorf("store: delete backend %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := requireAtLeastOneBackend(tx); err != nil {
			return fmt.Errorf("store: delete backend %s: %w", name, err)
		}
		if err := validateFleet(tx); err != nil {
			return fmt.Errorf("store: delete backend %s: %w", name, err)
		}
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
	if err := requireAtLeastOneEnabledRepo(tx); err != nil {
		return fmt.Errorf("store: upsert repo %s: %w", r.Name, err)
	}
	if err := validateFleet(tx); err != nil {
		return fmt.Errorf("store: upsert repo %s: %w", r.Name, err)
	}
	return tx.Commit()
}

// DeleteRepo removes a repo and all of its bindings. Returns an error if the
// deletion would leave no enabled repos.
func DeleteRepo(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete repo %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM bindings WHERE repo=?", name); err != nil {
		return fmt.Errorf("store: delete bindings for repo %s: %w", name, err)
	}
	res, err := tx.Exec("DELETE FROM repos WHERE name=?", name)
	if err != nil {
		return fmt.Errorf("store: delete repo %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := requireAtLeastOneEnabledRepo(tx); err != nil {
			return fmt.Errorf("store: delete repo %s: %w", name, err)
		}
	}
	return tx.Commit()
}
