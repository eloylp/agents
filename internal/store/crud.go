package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/robfig/cron/v3"
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

// ──── Agents ────────────────────────────────────────────────────────────────────────────────────

// ReadAgents returns all agents from the database, ordered by name.
func ReadAgents(db *sql.DB) ([]fleet.Agent, error) {
	var cfg config.Config
	if err := loadAgents(db, &cfg); err != nil {
		return nil, err
	}
	return cfg.Agents, nil
}

// UpsertAgent inserts or replaces a single agent definition.
// The agent name and related fields are normalized (lowercase, trimmed) before
// writing so the stored values match the canonical form that AgentByName and
// registerJobs expect, keeping live behavior consistent with startup.
func UpsertAgent(db *sql.DB, a fleet.Agent) error {
	fleet.NormalizeAgent(&a)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert agent %s: begin: %w", a.Name, err)
	}
	defer tx.Rollback()
	if err := importAgents(tx, []fleet.Agent{a}, true); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert agent %s: %v", a.Name, err)}
	}
	return tx.Commit()
}

// DeleteAgent removes the agent with the given name. It is not an error to
// delete a name that does not exist. Returns an error if the agent is still
// referenced by any repo binding or can_dispatch list, or if it is the last agent.
func DeleteAgent(db *sql.DB, name string) error {
	return DeleteWorkspaceAgent(db, fleet.DefaultWorkspaceID, name)
}

// DeleteAgentCascade removes the agent and all repo bindings that reference it
// in a single transaction. It still fails if removing the agent would leave
// zero agents, or if any other agent's can_dispatch list still references it
// (cascading across agent relationships would silently reshape the dispatch
// graph; the user should opt in explicitly).
func DeleteAgentCascade(db *sql.DB, name string) error {
	return DeleteWorkspaceAgentCascade(db, fleet.DefaultWorkspaceID, name)
}

func DeleteWorkspaceAgent(db *sql.DB, workspaceID, name string) error {
	return deleteAgent(db, workspaceID, name, false)
}

func DeleteWorkspaceAgentCascade(db *sql.DB, workspaceID, name string) error {
	return deleteAgent(db, workspaceID, name, true)
}

func deleteAgent(db *sql.DB, workspaceID, name string, cascade bool) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete agent %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if cascade {
		if _, err := tx.Exec("DELETE FROM bindings WHERE workspace_id=? AND agent=?", workspaceID, name); err != nil {
			return fmt.Errorf("store: delete agent %s: cascade bindings: %w", name, err)
		}
	} else if refs, err := bindingsReferencing(tx, workspaceID, name); err != nil {
		return fmt.Errorf("store: delete agent %s: check bindings: %w", name, err)
	} else if len(refs) > 0 {
		// Surface this as an ErrConflict rather than letting the raw FK
		// constraint fire. Callers can show the referenced repos and
		// offer a cascade without parsing SQLite error strings.
		distinct := slices.Compact(slices.Sorted(slices.Values(refs)))
		list := strings.Join(distinct, ", ")
		if len(distinct) > 8 {
			list = strings.Join(distinct[:8], ", ") + fmt.Sprintf(", and %d more", len(distinct)-8)
		}
		return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: still referenced by %d binding(s) across %d repo(s) (%s); use cascade to remove them", name, len(refs), len(distinct), list)}
	}
	res, err := tx.Exec("DELETE FROM agents WHERE workspace_id=? AND name=?", workspaceID, name)
	if err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := requireAtLeastOne(tx, "SELECT COUNT(*) FROM agents", "agents", "config: at least one agent is required"); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: %v", name, err)}
		}
		if err := validateFleet(tx); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: %v", name, err)}
		}
	}
	return tx.Commit()
}

// bindingsReferencing returns the repo names of every binding that points at
// the given agent. Used by the non-cascade delete path to produce a typed
// conflict error instead of a raw FK failure.
func bindingsReferencing(q querier, workspaceID, agentName string) ([]string, error) {
	rows, err := q.Query("SELECT repo FROM bindings WHERE workspace_id=? AND agent=?", workspaceID, agentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
		err := queryCatalogIDByScopeName(db, "skills", s.WorkspaceID, s.Repo, s.Name).Scan(&existingID)
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
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importSkills(tx, map[string]fleet.Skill{name: s}); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert skill %s: %v", name, err)}
	}
	return tx.Commit()
}

// DeleteSkill removes the skill with the given name. Returns an error if any
// agent still references the skill.
func DeleteSkill(db *sql.DB, name string) error {
	name = fleet.NormalizeSkillName(name)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete skill %s: begin: %w", name, err)
	}
	defer tx.Rollback()
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
	if err := validateFleet(tx); err != nil {
		return &ErrConflict{Msg: fmt.Sprintf("store: delete skill %s: %v", name, err)}
	}
	return tx.Commit()
}

// ──── Backends ───────────────────────────────────────────────────────────────────────────────────────

// ReadBackends returns all AI backend configurations from the database.
func ReadBackends(db *sql.DB) (map[string]fleet.Backend, error) {
	var cfg config.Config
	if err := loadBackends(db, &cfg); err != nil {
		return nil, err
	}
	if cfg.Daemon.AIBackends == nil {
		return map[string]fleet.Backend{}, nil
	}
	return cfg.Daemon.AIBackends, nil
}

// UpsertBackend inserts or replaces a single AI backend configuration.
// Before writing, the backend is fully normalized to match what startup
// produces: the name is lowercased and trimmed, Command is trimmed, blank env
// keys are removed, and zero-value numeric fields are filled with startup
// defaults (timeout_seconds 0 → 600, max_prompt_chars 0 → 12000). This
// ensures the stored values are already in canonical form so that live
// behavior never diverges from a post-restart load.
func UpsertBackend(db *sql.DB, name string, b fleet.Backend) error {
	name = fleet.NormalizeBackendName(name)
	fleet.NormalizeBackend(&b)
	fleet.ApplyBackendDefaults(&b)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importBackends(tx, map[string]fleet.Backend{name: b}); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert backend %s: %v", name, err)}
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
		if err := requireAtLeastOne(tx, "SELECT COUNT(*) FROM backends", "backends", "config: at least one backend entry is required"); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete backend %s: %v", name, err)}
		}
		if err := validateFleet(tx); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete backend %s: %v", name, err)}
		}
	}
	return tx.Commit()
}

// ──── Prompts ───────────────────────────────────────────────────────────────────────────────────────

// UpsertPrompt inserts or replaces one prompt catalog entry. Existing rows keep
// their stable id while content, description, and scope fields are updated.
func UpsertPrompt(db *sql.DB, p fleet.Prompt) (fleet.Prompt, error) {
	p.Name = fleet.NormalizePromptName(p.Name)
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	if p.WorkspaceID != "" {
		p.WorkspaceID = fleet.NormalizeWorkspaceID(p.WorkspaceID)
	}
	p.Repo = fleet.NormalizeRepoName(p.Repo)
	p.Description = strings.TrimSpace(p.Description)
	p.Content = strings.TrimSpace(p.Content)
	if p.Name == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt name is required"}
	}
	if p.WorkspaceID == "" && p.Repo != "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt repo scope requires workspace_id"}
	}
	tx, err := db.Begin()
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: begin: %w", p.Name, err)
	}
	defer tx.Rollback()

	var existingID string
	if p.ID != "" {
		err = tx.QueryRow("SELECT id FROM prompts WHERE id=?", p.ID).Scan(&existingID)
	} else {
		err = queryPromptByScopeName(tx, p.WorkspaceID, p.Repo, p.Name).Scan(&existingID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: read existing: %w", p.Name, err)
	}
	if existingID != "" {
		p.ID = existingID
	} else if p.ID == "" {
		id, err := derivedPromptID(p)
		if err != nil {
			return fleet.Prompt{}, &ErrValidation{Msg: fmt.Sprintf("prompt %q: %v", p.Name, err)}
		}
		p.ID = id
	}
	if err := validateEntityID(p.ID); err != nil {
		return fleet.Prompt{}, &ErrValidation{Msg: fmt.Sprintf("prompt %q: %v", p.Name, err)}
	}
	if _, err := tx.Exec(`
		INSERT INTO prompts (id, workspace_id, repo, name, description, content, updated_at)
		VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			repo = excluded.repo,
			name = excluded.name,
			description = excluded.description,
			content = excluded.content,
			updated_at = datetime('now')`,
		p.ID, p.WorkspaceID, p.Repo, p.Name, p.Description, p.Content,
	); err != nil {
		if isUniqueConstraint(err) {
			return fleet.Prompt{}, &ErrConflict{Msg: fmt.Sprintf("prompt name %q is already used by another prompt in that scope", p.Name)}
		}
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: %w", p.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: commit: %w", p.Name, err)
	}
	return p, nil
}

// ReadPrompt resolves a prompt by stable id first, then by legacy global display
// name. Scoped prompts may share names, so callers that need deterministic
// addressing must pass the stable id.
func ReadPrompt(db *sql.DB, ref string) (fleet.Prompt, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt id is required"}
	}
	var p fleet.Prompt
	row := db.QueryRow(`
		SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name, description, content
		FROM prompts
		WHERE id=?`, ref)
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content)
	if err == nil {
		return p, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s by id: %w", ref, err)
	}
	name := fleet.NormalizePromptName(ref)
	row = db.QueryRow(`
		SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name, description, content
		FROM prompts
		WHERE workspace_id IS NULL AND repo IS NULL AND name=?`, name)
	err = row.Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s by global name: %w", ref, err)
	}
	return p, nil
}

// DeletePrompt removes one prompt addressed by stable id, with a compatibility
// fallback for legacy global display names. A prompt referenced by any agent
// cannot be deleted because agents must always point at existing prompt content.
func DeletePrompt(db *sql.DB, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return &ErrValidation{Msg: "prompt id is required"}
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: begin: %w", ref, err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRow("SELECT id FROM prompts WHERE id=?", ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		name := fleet.NormalizePromptName(ref)
		err = queryPromptByScopeName(tx, "", "", name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: lookup: %w", ref, err)
	}
	refs, err := agentsReferencingPrompt(tx, id)
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: check agents: %w", ref, err)
	}
	if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("prompt %q is referenced by %d agent(s): %s", ref, len(refs), formatReferenceList(refs))}
	}
	if _, err := tx.Exec("DELETE FROM prompts WHERE id=?", id); err != nil {
		return fmt.Errorf("store: delete prompt %s: %w", ref, err)
	}
	return tx.Commit()
}

func agentsReferencingPrompt(q querier, promptID string) ([]string, error) {
	rows, err := q.Query("SELECT workspace_id, name FROM agents WHERE prompt_id=? ORDER BY workspace_id, name", promptID)
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

func workspaceAgentRef(workspaceID, name string) string {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	if workspaceID == "" {
		workspaceID = fleet.DefaultWorkspaceID
	}
	return workspaceID + "/" + name
}

func formatReferenceList(refs []string) string {
	refs = slices.Compact(slices.Sorted(slices.Values(refs)))
	if len(refs) <= 8 {
		return strings.Join(refs, ", ")
	}
	return strings.Join(refs[:8], ", ") + fmt.Sprintf(", and %d more", len(refs)-8)
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

// ──── Repos ────────────────────────────────────────────────────────────────────────────────────────

// ReadRepos returns all repos (with bindings) from the database.
func ReadRepos(db *sql.DB) ([]fleet.Repo, error) {
	var cfg config.Config
	if err := loadRepos(db, &cfg); err != nil {
		return nil, err
	}
	return cfg.Repos, nil
}

// UpsertRepo inserts or replaces a repo and its bindings. Bindings are
// replaced wholesale: any existing bindings for the repo are removed before
// the new list is written. The repo name and binding agents/events are
// normalized (trimmed / lowercased) before writing.
func UpsertRepo(db *sql.DB, r fleet.Repo) error {
	fleet.NormalizeRepo(&r)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert repo %s: begin: %w", r.Name, err)
	}
	defer tx.Rollback()
	if err := importRepos(tx, []fleet.Repo{r}); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert repo %s: %v", r.Name, err)}
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
	if err := importAgents(tx, agents, true); err != nil {
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
	if err := importAgents(tx, agents, false); err != nil {
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
func ImportConfig(db *sql.DB, cfg *config.Config, budgets []TokenBudget) error {
	return importConfig(db, cfg, budgets, false)
}

// ReplaceConfig replaces the workspace-aware YAML import/export shape in a
// single transaction. The default workspace row is retained as the compatibility
// fallback, but all dependent mutable fleet rows are pruned before import.
func ReplaceConfig(db *sql.DB, cfg *config.Config, budgets []TokenBudget) error {
	return importConfig(db, cfg, budgets, true)
}

func importConfig(db *sql.DB, cfg *config.Config, budgets []TokenBudget, replace bool) error {
	normalizedSkills, normalizedBackends := normalizeFleet(cfg.Agents, cfg.Repos, cfg.Skills, cfg.Backends)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: import config: begin: %w", err)
	}
	defer tx.Rollback()

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
	if err := importAgents(tx, cfg.Agents, true); err != nil {
		return err
	}
	if err := importRepos(tx, cfg.Repos); err != nil {
		return err
	}
	if err := importTokenBudgetsTx(tx, budgets, replace); err != nil {
		return err
	}
	if err := validateFleetConstraints(tx, "import", cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

// ──── Bindings (atomic per-item CRUD) ────────────────────────────────────────────

// validateBindingShape checks the trigger-exclusivity and event-kind invariants
// for a single binding, without requiring a full repo context. Returns an
// *ErrValidation on failure so HTTP handlers can map it to 400 Bad Request.
func validateBindingShape(b fleet.Binding) error {
	if strings.TrimSpace(b.Agent) == "" {
		return &ErrValidation{Msg: "agent is required"}
	}
	n := b.TriggerCount()
	if n == 0 {
		return &ErrValidation{Msg: "binding has no trigger (set cron, labels, or events)"}
	}
	if n > 1 {
		return &ErrValidation{Msg: "binding mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger"}
	}
	if b.IsCron() {
		if _, err := cronParser.Parse(b.Cron); err != nil {
			return &ErrValidation{Msg: fmt.Sprintf("invalid cron expression %q: %v", b.Cron, err)}
		}
	}
	return nil
}

// normalizeBinding lowercases/trims agent and event names so writes match the
// canonical form the daemon derives at boot (see normalize() in config.go).
func normalizeBinding(b *fleet.Binding) {
	b.Agent = strings.ToLower(strings.TrimSpace(b.Agent))
	b.Cron = strings.TrimSpace(b.Cron)
	for i := range b.Events {
		b.Events[i] = strings.ToLower(strings.TrimSpace(b.Events[i]))
	}
}

// CreateBinding inserts a new binding row for the given repo and returns the
// generated ID along with the canonical (normalized) Binding the store
// persisted. The binding must satisfy trigger-exclusivity, cron parseability,
// and repo/agent reference checks; the repo and agent must both exist.
//
// Validation failures surface as *ErrValidation (HTTP 400). Missing repo/agent
// references surface as *ErrNotFound (HTTP 404). The caller is responsible for
// holding the store mutex and reloading cron schedules after success.
func CreateBinding(db *sql.DB, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	return CreateWorkspaceBinding(db, fleet.DefaultWorkspaceID, repoName, b)
}

func CreateWorkspaceBinding(db *sql.DB, workspaceID, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repoName = fleet.NormalizeRepoName(repoName)
	normalizeBinding(&b)
	if err := validateBindingShape(b); err != nil {
		return 0, fleet.Binding{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: begin: %w", err)
	}
	defer tx.Rollback()

	// Verify the repo exists so the FK violation surfaces as a typed error.
	var repoExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)", workspaceID, repoName).Scan(&repoExists); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: lookup repo: %w", err)
	}
	if !repoExists {
		return 0, fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("repo %q not found", repoName)}
	}
	var agentExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE workspace_id=? AND name=?)", workspaceID, b.Agent).Scan(&agentExists); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: lookup agent: %w", err)
	}
	if !agentExists {
		return 0, fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("unknown agent %q", b.Agent)}
	}

	labels, err := json.Marshal(nilSafeStrings(b.Labels))
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: marshal labels: %w", err)
	}
	events, err := json.Marshal(nilSafeStrings(b.Events))
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: marshal events: %w", err)
	}
	enabled := bindingEnabledInt(b.Enabled)
	res, err := tx.Exec(
		`INSERT INTO bindings(workspace_id,repo,agent,labels,events,cron,enabled) VALUES (?,?,?,?,?,?,?)`,
		workspaceID, repoName, b.Agent, string(labels), string(events), b.Cron, enabled,
	)
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: last insert id: %w", err)
	}
	if err := validateFleet(tx); err != nil {
		return 0, fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("store: create binding: %v", err)}
	}
	if err := tx.Commit(); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: commit: %w", err)
	}
	b.ID = id
	return id, b, nil
}

// UpdateBinding replaces the row identified by id with the new values. All
// binding fields (agent, labels, events, cron, enabled) are overwritten.
// Returns *ErrNotFound when no row matches, *ErrValidation for bad shapes or
// unknown agent refs.
//
// Callers hold the store mutex and reload cron afterwards.
func UpdateBinding(db *sql.DB, id int64, b fleet.Binding) (fleet.Binding, error) {
	normalizeBinding(&b)
	if err := validateBindingShape(b); err != nil {
		return fleet.Binding{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: begin: %w", err)
	}
	defer tx.Rollback()

	var workspaceID string
	if err := tx.QueryRow("SELECT workspace_id FROM bindings WHERE id=?", id).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
		}
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup workspace: %w", err)
	}
	var agentExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE workspace_id=? AND name=?)", workspaceID, b.Agent).Scan(&agentExists); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup agent: %w", err)
	}
	if !agentExists {
		return fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("unknown agent %q", b.Agent)}
	}

	labels, err := json.Marshal(nilSafeStrings(b.Labels))
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: marshal labels: %w", err)
	}
	events, err := json.Marshal(nilSafeStrings(b.Events))
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: marshal events: %w", err)
	}
	enabled := bindingEnabledInt(b.Enabled)
	res, err := tx.Exec(
		`UPDATE bindings SET agent=?, labels=?, events=?, cron=?, enabled=? WHERE id=?`,
		b.Agent, string(labels), string(events), b.Cron, enabled, id,
	)
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: rows affected: %w", err)
	}
	if n == 0 {
		return fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
	}

	var repoName string
	if err := tx.QueryRow("SELECT repo FROM bindings WHERE id=?", id).Scan(&repoName); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup repo: %w", err)
	}

	if err := validateFleet(tx); err != nil {
		return fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("store: update binding: %v", err)}
	}
	if err := tx.Commit(); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: commit: %w", err)
	}
	b.ID = id
	return b, nil
}

// DeleteBinding removes the row with the given id. Returns *ErrNotFound if
// no row matches. Post-delete validateFleet runs to catch any cross-entity
// invariant violations.
//
// Callers hold the store mutex and reload cron afterwards.
func DeleteBinding(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete binding: begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM bindings WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("store: delete binding: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete binding: rows affected: %w", err)
	}
	if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
	}
	if err := validateFleet(tx); err != nil {
		return &ErrConflict{Msg: fmt.Sprintf("store: delete binding: %v", err)}
	}
	return tx.Commit()
}

// ReadBinding fetches a single binding by ID along with its parent repo name.
// Returns found=false when the row does not exist; errors only reflect
// unexpected I/O failures.
func ReadBinding(db *sql.DB, id int64) (repoName string, b fleet.Binding, found bool, err error) {
	_, repoName, b, found, err = ReadWorkspaceBinding(db, id)
	return repoName, b, found, err
}

func ReadWorkspaceBinding(db *sql.DB, id int64) (workspaceID, repoName string, b fleet.Binding, found bool, err error) {
	var labelsJSON, eventsJSON, cron, agent string
	var enabled int
	err = db.QueryRow(
		"SELECT workspace_id,repo,agent,labels,events,cron,enabled FROM bindings WHERE id=?", id,
	).Scan(&workspaceID, &repoName, &agent, &labelsJSON, &eventsJSON, &cron, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fleet.Binding{}, false, nil
	}
	if err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: %w", id, err)
	}
	var labels []string
	if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: parse labels: %w", id, err)
	}
	var events []string
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: parse events: %w", id, err)
	}
	b = fleet.Binding{
		ID:     id,
		Agent:  agent,
		Labels: labels,
		Events: events,
		Cron:   cron,
	}
	if enabled == 0 {
		f := false
		b.Enabled = &f
	}
	return workspaceID, repoName, b, true, nil
}

// nilSafeStrings normalises nil to empty slices so JSON marshalling stays
// stable ([] rather than null). The webhook layer has the same helper; duped
// here so the store package does not import it.
func nilSafeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DeleteRepo removes a repo and all of its bindings. Deleting the last enabled
// (or only) repo is allowed, see issue #302; the daemon runs cleanly with zero
// enabled repos.
func DeleteRepo(db *sql.DB, name string) error {
	return DeleteWorkspaceRepo(db, fleet.DefaultWorkspaceID, name)
}

func DeleteWorkspaceRepo(db *sql.DB, workspaceID, name string) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete repo %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM bindings WHERE workspace_id=? AND repo=?", workspaceID, name); err != nil {
		return fmt.Errorf("store: delete bindings for repo %s: %w", name, err)
	}
	if _, err := tx.Exec("DELETE FROM repos WHERE workspace_id=? AND name=?", workspaceID, name); err != nil {
		return fmt.Errorf("store: delete repo %s: %w", name, err)
	}
	return tx.Commit()
}
