package store

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// Import writes cfg into the database, upserting every entity. Existing rows
// are replaced (INSERT OR REPLACE). Prompt content lives in prompts; agents
// persist only prompt catalog references.
//
// Secrets (WebhookSecret) are NOT written, only the env-var name
// (WebhookSecretEnv) is stored. The secret is re-resolved from the
// environment at Load time.
func Import(db *sql.DB, cfg *config.Config) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store import: begin: %w", err)
	}
	defer tx.Rollback()

	backends := cfg.Daemon.AIBackends
	if len(backends) == 0 {
		backends = cfg.Backends
	}
	if err := importBackends(tx, backends); err != nil {
		return err
	}
	if err := importRuntimeSettings(tx, cfg.Runtime); err != nil {
		return err
	}
	if err := importSkills(tx, cfg.Skills); err != nil {
		return err
	}
	if err := importGuardrails(tx, cfg.Guardrails); err != nil {
		return err
	}
	if err := importWorkspaces(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importWorkspaceGuardrails(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importPrompts(tx, cfg.Prompts); err != nil {
		return err
	}
	if err := importReferencedWorkspaces(tx, cfg.Agents, cfg.Repos); err != nil {
		return err
	}
	if err := importAgents(tx, cfg.Agents); err != nil {
		return err
	}
	if err := importRepos(tx, cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

// normalizeFleet normalizes agents and repos in-place and returns new maps
// with normalized skills and backends. Called by ImportAll and ReplaceAll.
func normalizeFleet(
	agents []fleet.Agent,
	repos []fleet.Repo,
	skills map[string]fleet.Skill,
	backends map[string]fleet.Backend,
) (map[string]fleet.Skill, map[string]fleet.Backend) {
	for i := range agents {
		fleet.NormalizeAgent(&agents[i])
	}
	for i := range repos {
		fleet.NormalizeRepo(&repos[i])
	}
	normalizedSkills := make(map[string]fleet.Skill, len(skills))
	for name, s := range skills {
		name = fleet.NormalizeSkillName(name)
		fleet.NormalizeSkill(&s)
		normalizedSkills[name] = s
	}
	normalizedBackends := make(map[string]fleet.Backend, len(backends))
	for name, b := range backends {
		name = fleet.NormalizeBackendName(name)
		fleet.NormalizeBackend(&b)
		fleet.ApplyBackendDefaults(&b)
		normalizedBackends[name] = b
	}
	return normalizedSkills, normalizedBackends
}

// ImportAll upserts agents, repos, skills, backends, guardrails, and token
// budgets in a single atomic transaction. If any entity fails validation the
// entire import is rolled back and no writes are persisted. Each entity is
// normalized before writing, consistent with the normalization the individual
// Upsert* helpers apply.
func ImportAll(
	db *sql.DB,
	agents []fleet.Agent,
	repos []fleet.Repo,
	skills map[string]fleet.Skill,
	backends map[string]fleet.Backend,
	guardrails []fleet.Guardrail,
	budgets []TokenBudget,
) error {
	normalizedSkills, normalizedBackends := normalizeFleet(agents, repos, skills, backends)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: import: begin: %w", err)
	}
	defer tx.Rollback()
	if err := importSkills(tx, normalizedSkills); err != nil {
		return err
	}
	if err := importBackends(tx, normalizedBackends); err != nil {
		return err
	}
	if err := importGuardrails(tx, guardrails); err != nil {
		return err
	}
	if err := importReferencedWorkspaces(tx, agents, repos); err != nil {
		return err
	}
	if err := importAgents(tx, agents); err != nil {
		return err
	}
	if err := importRepos(tx, repos); err != nil {
		return err
	}
	if err := importTokenBudgetsTx(tx, budgets, false); err != nil {
		return err
	}
	if err := validateFleetConstraints(tx, "import", repos); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceAll replaces the entire fleet configuration atomically. All
// existing agents, repos, skills, backends, bindings, operator-added
// guardrails, and token budgets are deleted and replaced with the provided
// entities. Built-in guardrails are kept across replace (their is_builtin
// and default_content are migration-managed and not editable from YAML);
// when the inbound guardrails list contains a name that matches a built-in,
// the upsert path updates content / description / enabled / position while
// preserving the built-in flags.
func ReplaceAll(
	db *sql.DB,
	agents []fleet.Agent,
	repos []fleet.Repo,
	skills map[string]fleet.Skill,
	backends map[string]fleet.Backend,
	guardrails []fleet.Guardrail,
	budgets []TokenBudget,
) error {
	normalizedSkills, normalizedBackends := normalizeFleet(agents, repos, skills, backends)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: replace: begin: %w", err)
	}
	defer tx.Rollback()

	// Delete in dependency order: bindings reference repos and agents.
	for _, tbl := range []string{"bindings", "repos", "agents", "skills", "backends"} {
		if _, err := tx.Exec("DELETE FROM " + tbl); err != nil {
			return fmt.Errorf("store: replace: truncate %s: %w", tbl, err)
		}
	}
	// Operator-added guardrails are wiped; built-ins survive so the
	// migration-managed default_content and is_builtin flags persist.
	if _, err := tx.Exec("DELETE FROM guardrails WHERE is_builtin = 0"); err != nil {
		return fmt.Errorf("store: replace: truncate operator guardrails: %w", err)
	}

	if err := importSkills(tx, normalizedSkills); err != nil {
		return err
	}
	if err := importBackends(tx, normalizedBackends); err != nil {
		return err
	}
	if err := importGuardrails(tx, guardrails); err != nil {
		return err
	}
	if err := importReferencedWorkspaces(tx, agents, repos); err != nil {
		return err
	}
	if err := importAgents(tx, agents); err != nil {
		return err
	}
	if err := importRepos(tx, repos); err != nil {
		return err
	}
	if err := importTokenBudgetsTx(tx, budgets, true); err != nil {
		return err
	}
	if err := validateFleetConstraints(tx, "replace", repos); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportConfig upserts the workspace-aware YAML import/export shape in a
// single transaction. It includes prompt catalog entries and workspace
// guardrail references in addition to the legacy fleet sections.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and complete-config validation, or use ImportConfigTx
// inside a service-owned transaction.
func ImportConfig(db *sql.DB, cfg *config.Config, budgets []TokenBudget) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: import config: begin: %w", err)
	}
	defer tx.Rollback()
	if err := ImportConfigTx(tx, cfg, budgets); err != nil {
		return err
	}
	if err := validateFleetConstraints(tx, "import", cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceConfig replaces the workspace-aware YAML import/export shape in a
// single transaction. The default workspace row is retained as the compatibility
// fallback, but all dependent mutable fleet rows are pruned before import.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and complete-config validation, or use ReplaceConfigTx
// inside a service-owned transaction.
func ReplaceConfig(db *sql.DB, cfg *config.Config, budgets []TokenBudget) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: import config: begin: %w", err)
	}
	defer tx.Rollback()
	if err := ReplaceConfigTx(tx, cfg, budgets); err != nil {
		return err
	}
	if err := validateFleetConstraints(tx, "replace", cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

func ImportConfigTx(tx *sql.Tx, cfg *config.Config, budgets []TokenBudget) error {
	return importConfigTx(tx, cfg, budgets, false)
}

func ReplaceConfigTx(tx *sql.Tx, cfg *config.Config, budgets []TokenBudget) error {
	return importConfigTx(tx, cfg, budgets, true)
}

func importConfigTx(tx *sql.Tx, cfg *config.Config, budgets []TokenBudget, replace bool) error {
	normalizedSkills, normalizedBackends := normalizeFleet(cfg.Agents, cfg.Repos, cfg.Skills, cfg.Backends)

	if replace {
		for _, tbl := range []string{"bindings", "repos", "agents", "token_budgets"} {
			if _, err := tx.Exec("DELETE FROM " + tbl); err != nil {
				return fmt.Errorf("store: replace config: truncate %s: %w", tbl, err)
			}
		}
		if _, err := tx.Exec("DELETE FROM prompts"); err != nil {
			return fmt.Errorf("store: replace config: truncate prompts: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM workspace_guardrails"); err != nil {
			return fmt.Errorf("store: replace config: truncate workspace guardrails: %w", err)
		}
		if err := seedWorkspaceGuardrails(tx, fleet.DefaultWorkspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM workspaces WHERE id <> ?", fleet.DefaultWorkspaceID); err != nil {
			return fmt.Errorf("store: replace config: truncate workspaces: %w", err)
		}
		for _, tbl := range []string{"skills", "backends"} {
			if _, err := tx.Exec("DELETE FROM " + tbl); err != nil {
				return fmt.Errorf("store: replace config: truncate %s: %w", tbl, err)
			}
		}
		if _, err := tx.Exec("DELETE FROM guardrails WHERE is_builtin = 0"); err != nil {
			return fmt.Errorf("store: replace config: truncate operator guardrails: %w", err)
		}
	}

	if err := importSkills(tx, normalizedSkills); err != nil {
		return err
	}
	if err := importBackends(tx, normalizedBackends); err != nil {
		return err
	}
	if err := importRuntimeSettings(tx, cfg.Runtime); err != nil {
		return err
	}
	if err := importGuardrails(tx, cfg.Guardrails); err != nil {
		return err
	}
	if err := importWorkspaces(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importWorkspaceGuardrails(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importPrompts(tx, cfg.Prompts); err != nil {
		return err
	}
	if err := importReferencedWorkspaces(tx, cfg.Agents, cfg.Repos); err != nil {
		return err
	}
	if err := importAgents(tx, cfg.Agents); err != nil {
		return err
	}
	if err := importRepos(tx, cfg.Repos); err != nil {
		return err
	}
	if err := importTokenBudgetsTx(tx, budgets, replace); err != nil {
		return err
	}
	return nil
}
