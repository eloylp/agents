package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// ErrValidation is returned by Upsert* operations when the mutation is
// rejected due to invalid field values or cross-entity reference failures.
// HTTP handlers should map this to 400 Bad Request.
type ErrValidation struct{ Msg string }

func (e *ErrValidation) Error() string { return e.Msg }

// ErrConflict is returned by Delete* operations when the deletion would
// violate a cardinality invariant ("at least one" minimum) or a
// referenced-by constraint (entity still in use by another entity).
// HTTP handlers should map this to 409 Conflict.
type ErrConflict struct{ Msg string }

func (e *ErrConflict) Error() string { return e.Msg }

// ErrNotFound is returned by per-item operations when the addressed row does
// not exist. HTTP handlers should map this to 404 Not Found.
type ErrNotFound struct{ Msg string }

func (e *ErrNotFound) Error() string { return e.Msg }

// validateCronExpressions checks that every cron binding in repos can be
// parsed by the same parser the autonomous scheduler uses. Returns an
// ErrValidation if any expression is malformed.
func validateCronExpressions(repos []fleet.Repo) error {
	if err := fleet.ValidateRepoCronExpressions(repos); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: %v", err)}
	}
	return nil
}

// validateFleet reads all four mutable entity tables through q (a *sql.Tx in
// practice, so reads see the pending transaction state) and verifies that the
// post-mutation snapshot satisfies both field-level constraints and cross-entity
// reference consistency via config.ValidateEntities. Aggregate minimums ("at
// least one agent/repo/backend required") are NOT checked here; DELETE paths
// enforce those separately with requireAtLeastOne below.
func validateFleet(q querier) error {
	cfg, err := LoadFleetConfig(q)
	if err != nil {
		return err
	}
	backends := cfg.Daemon.AIBackends
	if backends == nil {
		backends = map[string]fleet.Backend{}
	}
	skills := cfg.Skills
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}
	if err := config.ValidateEntities(cfg.Agents, cfg.Repos, skills, backends); err != nil {
		return err
	}
	return config.ValidateAgentCatalogVisibility(cfg.Agents, skills)
}

// LoadFleetConfig reads the mutable fleet snapshot through q. Service uses
// this against an open transaction so validation sees pending writes before
// commit while store remains responsible only for SQL loading.
func LoadFleetConfig(q querier) (*config.Config, error) {
	var cfg config.Config
	if err := loadBackends(q, &cfg); err != nil {
		return nil, err
	}
	if err := loadSkills(q, &cfg); err != nil {
		return nil, err
	}
	if err := loadAgents(q, &cfg); err != nil {
		return nil, err
	}
	if err := loadRepos(q, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadFleetConfigTx(tx *sql.Tx) (*config.Config, error) {
	return LoadFleetConfig(tx)
}

// requireAtLeastOne fails if the COUNT query returns 0. entity names the table
// or set being counted (used in the scan-error message); zeroMsg is returned
// verbatim when the count is zero.
func requireAtLeastOne(q querier, countQuery, entity, zeroMsg string) error {
	var n int
	if err := q.QueryRow(countQuery).Scan(&n); err != nil {
		return fmt.Errorf("store: count %s: %w", entity, err)
	}
	if n == 0 {
		return errors.New(zeroMsg)
	}
	return nil
}

// validateFleetConstraints runs all post-write invariant checks shared by
// ImportAll and ReplaceAll: entity cross-references, minimum cardinality, and
// cron-expression parseability. op ("import" or "replace") is used verbatim in
// error messages.
//
// "At least one enabled repo" is intentionally NOT enforced here, disabling
// all repos is a legitimate user action (fleet maintenance, evaluating prompts
// on a different repo) and the daemon runs cleanly with zero enabled repos.
// See issue #302.
func validateFleetConstraints(q querier, op string, repos []fleet.Repo) error {
	if err := validateFleet(q); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: %s: %v", op, err)}
	}
	if err := requireAtLeastOne(q, "SELECT COUNT(*) FROM agents", "agents", "config: at least one agent is required"); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: %s: %v", op, err)}
	}
	if err := requireAtLeastOne(q, "SELECT COUNT(*) FROM backends", "backends", "config: at least one backend entry is required"); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: %s: %v", op, err)}
	}
	return validateCronExpressions(repos)
}
