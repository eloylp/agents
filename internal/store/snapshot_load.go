package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// Load reads all configuration from the database and returns a *config.Config
// ready for use. It applies the same defaults, normalization, secret resolution
// and validation as config.Load does for YAML.
func Load(db *sql.DB) (*config.Config, error) {
	cfg := &config.Config{}

	if err := loadBackends(db, cfg); err != nil {
		return nil, err
	}
	if err := loadRuntimeSettings(db, cfg); err != nil {
		return nil, err
	}
	if err := loadSkills(db, cfg); err != nil {
		return nil, err
	}
	if err := loadWorkspaces(db, cfg); err != nil {
		return nil, err
	}
	if err := loadPrompts(db, cfg); err != nil {
		return nil, err
	}
	if err := loadAgents(db, cfg); err != nil {
		return nil, err
	}
	if err := loadRepos(db, cfg); err != nil {
		return nil, err
	}
	if err := loadGuardrails(db, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ReadSnapshot returns agents, repos, skills, and backends as a consistent
// point-in-time snapshot by reading all four within a single SQLite transaction.
// This prevents the race where a concurrent /api/store write commits between
// reads, producing a mixed snapshot that can cause spurious Reload failures or
// agents that see new definitions with stale skill/backend maps.
func ReadSnapshot(db *sql.DB) ([]fleet.Agent, []fleet.Repo, map[string]fleet.Skill, map[string]fleet.Backend, error) {
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
		cfg.Skills = map[string]fleet.Skill{}
	}
	if cfg.Daemon.AIBackends == nil {
		cfg.Daemon.AIBackends = map[string]fleet.Backend{}
	}
	return cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends, nil
}

// ImportCount holds row counts written during an Import call, for progress
// logging.
type ImportCount struct {
	Backends int
	Skills   int
	Agents   int
	Repos    int
	Bindings int
}

// String returns a human-readable summary of imported row counts.
func (c ImportCount) String() string {
	return fmt.Sprintf("imported %d backends, %d skills, %d agents, %d repos, %d bindings",
		c.Backends, c.Skills, c.Agents, c.Repos, c.Bindings)
}

// CountFrom returns an ImportCount reflecting the current row counts in db.
func CountFrom(db *sql.DB) (ImportCount, error) {
	var c ImportCount
	tables := []struct {
		table string
		dest  *int
	}{
		{"backends", &c.Backends},
		{"skills", &c.Skills},
		{"agents", &c.Agents},
		{"repos", &c.Repos},
		{"bindings", &c.Bindings},
	}
	for _, t := range tables {
		if err := db.QueryRow("SELECT COUNT(*) FROM " + t.table).Scan(t.dest); err != nil {
			return c, fmt.Errorf("store: count %s: %w", t.table, err)
		}
	}
	return c, nil
}

// LoadAndValidate is the full startup path when --db is used. It reads the
// config from the database, resolves secrets from env vars, and runs the same
// validation as config.Load. The returned *config.Config is ready to use.
func LoadAndValidate(db *sql.DB) (*config.Config, error) {
	cfg, err := Load(db)
	if err != nil {
		return nil, err
	}
	return config.FinishLoad(cfg)
}
