package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"

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

// cronParser is the same 5-field parser used by the autonomous scheduler.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// validateCronExpressions checks that every cron binding in repos can be
// parsed by the same parser the autonomous scheduler uses. Returns an
// ErrValidation if any expression is malformed.
func validateCronExpressions(repos []fleet.Repo) error {
	for _, r := range repos {
		for _, b := range r.Use {
			if !b.IsCron() {
				continue
			}
			if _, err := cronParser.Parse(b.Cron); err != nil {
				return &ErrValidation{Msg: fmt.Sprintf("store: invalid cron expression %q for repo %q: %v", b.Cron, r.Name, err)}
			}
		}
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
		backends = map[string]fleet.Backend{}
	}
	skills := cfg.Skills
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}
	if err := config.ValidateEntities(cfg.Agents, cfg.Repos, skills, backends); err != nil {
		return err
	}
	return validateAgentCatalogVisibility(cfg.Agents, skills)
}

func validateAgentCatalogVisibility(agents []fleet.Agent, skills map[string]fleet.Skill) error {
	for _, a := range agents {
		workspaceID := fleet.NormalizeWorkspaceID(a.WorkspaceID)
		repo := ""
		if a.ScopeType == "repo" {
			repo = fleet.NormalizeRepoName(a.ScopeRepo)
		}
		for _, skillID := range a.Skills {
			skill, ok := skills[skillID]
			if !ok {
				continue
			}
			if skill.Repo != "" && repo == "" {
				return fmt.Errorf("workspace-scoped agent %q references repo-scoped skill %q without repo context", a.Name, skillID)
			}
			if !catalogVisibleToAgent(skill.WorkspaceID, skill.Repo, workspaceID, repo) {
				return fmt.Errorf("agent %q references skill %q outside its visible catalog scope", a.Name, skillID)
			}
		}
	}
	return nil
}

func catalogVisibleToAgent(itemWorkspace, itemRepo, agentWorkspace, agentRepo string) bool {
	itemWorkspace = strings.TrimSpace(itemWorkspace)
	itemRepo = strings.TrimSpace(itemRepo)
	if itemWorkspace == "" && itemRepo == "" {
		return true
	}
	if itemWorkspace != agentWorkspace {
		return false
	}
	return itemRepo == "" || (agentRepo != "" && itemRepo == agentRepo)
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
