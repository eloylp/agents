package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importAgents(tx *sql.Tx, agents []fleet.Agent) error {
	type agentRefs struct {
		id          string
		workspaceID string
		name        string
		skills      []agentSkillRef
		canDispatch []string
	}
	refs := make([]agentRefs, 0, len(agents))
	for _, a := range agents {
		fleet.NormalizeAgent(&a)
		if fleet.IsReservedAgentName(a.Name) {
			return &ErrValidation{Msg: fmt.Sprintf("config: agent name %q is reserved for daemon-managed internal agents", a.Name)}
		}
		workspaceID := fleet.NormalizeWorkspaceID(a.WorkspaceID)
		scopeType := a.ScopeType
		if scopeType == "" {
			scopeType = "workspace"
		}
		if err := validateAgentScalarRefs(tx, a, workspaceID, scopeType); err != nil {
			return err
		}
		id, err := stableAgentID(tx, workspaceID, a)
		if err != nil {
			return err
		}
		skillRefs, err := resolveAgentSkillRefs(tx, a, workspaceID, scopeType)
		if err != nil {
			return err
		}
		promptID, err := ensureAgentPrompt(tx, a)
		if err != nil {
			return err
		}
		allowPRs := boolToInt(a.AllowPRs)
		allowDispatch := boolToInt(a.AllowDispatch)
		allowMemory := bindingEnabledInt(a.AllowMemory)
		if _, err := tx.Exec(`
			INSERT INTO agents
			  (id,workspace_id,name,backend,model,prompt_id,scope_type,scope_repo,allow_prs,allow_dispatch,description,allow_memory)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id, name) DO UPDATE SET
				id = excluded.id,
				backend = excluded.backend,
				model = excluded.model,
				prompt_id = excluded.prompt_id,
				scope_type = excluded.scope_type,
				scope_repo = excluded.scope_repo,
				allow_prs = excluded.allow_prs,
				allow_dispatch = excluded.allow_dispatch,
				description = excluded.description,
				allow_memory = excluded.allow_memory`,
			id, workspaceID, a.Name, a.Backend, a.Model, promptID, scopeType, a.ScopeRepo,
			allowPRs, allowDispatch, a.Description, allowMemory,
		); err != nil {
			return fmt.Errorf("store import: upsert agent %s: %w", a.Name, err)
		}
		refs = append(refs, agentRefs{
			id:          id,
			workspaceID: workspaceID,
			name:        a.Name,
			skills:      skillRefs,
			canDispatch: nilSafeStrings(a.CanDispatch),
		})
	}
	for _, ref := range refs {
		if err := replaceAgentSkills(tx, ref.id, ref.name, ref.skills); err != nil {
			return err
		}
		if err := replaceAgentDispatches(tx, ref.id, ref.workspaceID, ref.name, ref.canDispatch); err != nil {
			return err
		}
	}
	return nil
}

type agentSkillRef struct {
	skillID string
}

func resolveAgentSkillRefs(tx *sql.Tx, a fleet.Agent, workspaceID, scopeType string) ([]agentSkillRef, error) {
	repo := ""
	if scopeType == "repo" {
		repo = a.ScopeRepo
	}
	refs := make([]agentSkillRef, 0, len(a.Skills))
	for _, raw := range a.Skills {
		ref, err := parseSkillRef(raw)
		if err != nil {
			return nil, fmt.Errorf("store import: agent %s skill %q: %w", a.Name, raw, err)
		}
		id, err := resolveVisibleCatalogRef(tx, "skills", ref, workspaceID, repo)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if skill, ok, readErr := readSkillScopeByID(tx, ref); readErr != nil {
					return nil, fmt.Errorf("store import: read skill %s for agent %s: %w", ref, a.Name, readErr)
				} else if ok {
					if skill.Repo != "" && repo == "" {
						return nil, &ErrValidation{Msg: fmt.Sprintf("workspace-scoped agent %q references repo-scoped skill %q without repo context", a.Name, ref)}
					}
					return nil, &ErrValidation{Msg: fmt.Sprintf("agent %q references skill %q outside its visible catalog scope", a.Name, ref)}
				}
				return nil, &ErrValidation{Msg: fmt.Sprintf("store import: agent %s references unknown skill %q", a.Name, ref)}
			}
			return nil, fmt.Errorf("store import: resolve skill %s for agent %s: %w", ref, a.Name, err)
		}
		refs = append(refs, agentSkillRef{skillID: id})
	}
	return refs, nil
}

func parseSkillRef(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	ref, _, found := strings.Cut(raw, "@")
	if found {
		return "", &ErrValidation{Msg: "skill version suffixes are no longer supported; reference the skill by name and publish rollback content when needed"}
	}
	ref = fleet.NormalizeSkillName(ref)
	if ref == "" {
		return "", &ErrValidation{Msg: "skill ref is required"}
	}
	return ref, nil
}

func ensureAgentPrompt(tx *sql.Tx, a fleet.Agent) (string, error) {
	if a.PromptID != "" {
		scopeRepo := ""
		if a.ScopeType == "repo" {
			scopeRepo = a.ScopeRepo
		}
		id, err := resolveVisibleCatalogID(tx, "prompts", a.PromptID, a.WorkspaceID, scopeRepo)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", &ErrValidation{Msg: fmt.Sprintf("store import: agent %s references unknown prompt_id %q", a.Name, a.PromptID)}
			}
			return "", fmt.Errorf("store import: read prompt_id %s for agent %s: %w", a.PromptID, a.Name, err)
		}
		return id, nil
	}
	if a.PromptRef == "" {
		return "", fmt.Errorf("store import: agent %s: prompt_id or prompt_ref is required", a.Name)
	}
	scopeRepo := ""
	if a.ScopeType == "repo" {
		scopeRepo = a.ScopeRepo
	}
	existingID, err := resolveAgentPromptRef(tx, a.PromptRef, a.PromptScope, a.WorkspaceID, scopeRepo)
	if err == nil {
		return existingID, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", &ErrValidation{Msg: fmt.Sprintf("store import: agent %s references unknown prompt_ref %q", a.Name, a.PromptRef)}
	}
	return "", fmt.Errorf("store import: resolve prompt_ref %s for agent %s: %w", a.PromptRef, a.Name, err)
}

func stableAgentID(q querier, workspaceID string, a fleet.Agent) (string, error) {
	var existing string
	err := q.QueryRow("SELECT COALESCE(id, '') FROM agents WHERE workspace_id=? AND name=?", workspaceID, a.Name).Scan(&existing)
	if err == nil && existing != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store import: read agent %s id: %w", a.Name, err)
	}
	return newAgentID()
}

func newAgentID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store import: generate agent id: %w", err)
	}
	return "agent_" + hex.EncodeToString(b[:]), nil
}

func loadAgents(db querier, cfg *config.Config) error {
	rows, err := db.Query(`
		SELECT a.id,a.workspace_id,a.name,a.backend,a.model,COALESCE(p.ref, ''),COALESCE(p.name, ''),COALESCE(p.workspace_id, ''),COALESCE(p.repo, ''),a.scope_type,a.scope_repo,a.allow_prs,a.allow_dispatch,a.description,a.allow_memory
		FROM agents a
		LEFT JOIN prompts p ON p.id = a.prompt_id
		ORDER BY a.name`)
	if err != nil {
		return fmt.Errorf("store load: query agents: %w", err)
	}
	defer rows.Close()

	var agents []fleet.Agent
	var agentIDs []string
	for rows.Next() {
		var id, workspaceID, name, backend, model, promptID, promptRef, promptWorkspace, promptRepo, scopeType, scopeRepo, description string
		var allowPRs, allowDispatch, allowMemory int
		if err := rows.Scan(
			&id, &workspaceID, &name, &backend, &model, &promptID, &promptRef, &promptWorkspace, &promptRepo, &scopeType, &scopeRepo,
			&allowPRs, &allowDispatch, &description, &allowMemory,
		); err != nil {
			return fmt.Errorf("store load: scan agent: %w", err)
		}
		// Always materialise AllowMemory as a non-nil pointer so downstream
		// readers see a concrete bool reflecting the stored row, not the
		// "absent" sentinel that nil represents on inbound YAML/JSON paths.
		allowMem := intToBool(allowMemory)
		promptScope := ""
		if promptID != "" {
			promptScope = fleet.CatalogScopePath(promptWorkspace, promptRepo)
		}
		agents = append(agents, fleet.Agent{
			ID:            id,
			WorkspaceID:   workspaceID,
			Name:          name,
			Backend:       backend,
			Model:         model,
			PromptID:      promptID,
			PromptRef:     promptRef,
			PromptScope:   promptScope,
			ScopeType:     scopeType,
			ScopeRepo:     scopeRepo,
			AllowPRs:      intToBool(allowPRs),
			AllowDispatch: intToBool(allowDispatch),
			Description:   description,
			AllowMemory:   &allowMem,
		})
		agentIDs = append(agentIDs, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate agents: %w", err)
	}
	for i, id := range agentIDs {
		skills, _, err := loadAgentSkillRefs(db, id)
		if err != nil {
			return fmt.Errorf("store load: load agent %s skills: %w", agents[i].Name, err)
		}
		canDispatch, err := loadAgentDispatchRefs(db, id)
		if err != nil {
			return fmt.Errorf("store load: load agent %s can_dispatch: %w", agents[i].Name, err)
		}
		agents[i].Skills = skills
		agents[i].CanDispatch = canDispatch
	}
	cfg.Agents = agents
	return nil
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

// ListWorkspaceAgents returns one deterministic page of agents in a workspace.
func ListWorkspaceAgents(db *sql.DB, workspaceID string, limit, offset int) ([]fleet.Agent, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	limit, offset = clampPage(limit, offset)
	rows, err := db.Query(`
		SELECT a.id,a.workspace_id,a.name,a.backend,a.model,COALESCE(p.ref, ''),COALESCE(p.name, ''),COALESCE(p.workspace_id, ''),COALESCE(p.repo, ''),a.scope_type,a.scope_repo,a.allow_prs,a.allow_dispatch,a.description,a.allow_memory
		FROM agents a
		LEFT JOIN prompts p ON p.id = a.prompt_id
		WHERE a.workspace_id=?
		ORDER BY a.name
		LIMIT ? OFFSET ?`, workspaceID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	defer rows.Close()
	var agents []fleet.Agent
	var agentIDs []string
	for rows.Next() {
		var id, rowWorkspace, name, backend, model, promptID, promptRef, promptWorkspace, promptRepo, scopeType, scopeRepo, description string
		var allowPRs, allowDispatch, allowMemory int
		if err := rows.Scan(
			&id, &rowWorkspace, &name, &backend, &model, &promptID, &promptRef, &promptWorkspace, &promptRepo, &scopeType, &scopeRepo,
			&allowPRs, &allowDispatch, &description, &allowMemory,
		); err != nil {
			return nil, fmt.Errorf("store: list agents: scan: %w", err)
		}
		allowMem := intToBool(allowMemory)
		promptScope := ""
		if promptID != "" {
			promptScope = fleet.CatalogScopePath(promptWorkspace, promptRepo)
		}
		agents = append(agents, fleet.Agent{
			ID:            id,
			WorkspaceID:   rowWorkspace,
			Name:          name,
			Backend:       backend,
			Model:         model,
			PromptID:      promptID,
			PromptRef:     promptRef,
			PromptScope:   promptScope,
			ScopeType:     scopeType,
			ScopeRepo:     scopeRepo,
			AllowPRs:      intToBool(allowPRs),
			AllowDispatch: intToBool(allowDispatch),
			Description:   description,
			AllowMemory:   &allowMem,
		})
		agentIDs = append(agentIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list agents: iterate: %w", err)
	}
	for i, id := range agentIDs {
		skills, _, err := loadAgentSkillRefs(db, id)
		if err != nil {
			return nil, fmt.Errorf("store: list agents: load %s skills: %w", agents[i].Name, err)
		}
		canDispatch, err := loadAgentDispatchRefs(db, id)
		if err != nil {
			return nil, fmt.Errorf("store: list agents: load %s can_dispatch: %w", agents[i].Name, err)
		}
		agents[i].Skills = skills
		agents[i].CanDispatch = canDispatch
	}
	return agents, nil
}

func CountWorkspaceAgents(db *sql.DB, workspaceID string) (int, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id=?", workspaceID).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: count agents: %w", err)
	}
	return total, nil
}

// UpsertAgent inserts or replaces a single agent definition.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and post-write fleet validation, or use UpsertAgentTx
// inside a service-owned transaction.
//
// The agent name and related fields are normalized (lowercase, trimmed) before
// writing so the stored values match the canonical form that AgentByName and
// registerJobs expect, keeping live behavior consistent with startup.
func UpsertAgent(db *sql.DB, a fleet.Agent) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert agent %s: begin: %w", a.Name, err)
	}
	defer tx.Rollback()
	if err := UpsertAgentTx(tx, a); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert agent %s: %v", a.Name, err)}
	}
	return tx.Commit()
}

// UpsertAgentTx persists one normalized agent inside an existing transaction.
// Callers that own use-case orchestration should validate the post-write fleet
// snapshot before committing.
func UpsertAgentTx(tx *sql.Tx, a fleet.Agent) error {
	fleet.NormalizeAgent(&a)
	if err := importAgents(tx, []fleet.Agent{a}); err != nil {
		return err
	}
	return nil
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
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete agent %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	var existed bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE workspace_id=? AND name=?)", workspaceID, name).Scan(&existed); err != nil {
		return fmt.Errorf("store: delete agent %s: lookup: %w", name, err)
	}
	if err := DeleteWorkspaceAgentTx(tx, workspaceID, name, cascade); err != nil {
		return err
	}
	if existed {
		if err := requireAtLeastOne(tx, "SELECT COUNT(*) FROM agents", "agents", "config: at least one agent is required"); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: %v", name, err)}
		}
		if err := validateFleet(tx); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: %v", name, err)}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete agent %s: commit: %w", name, err)
	}
	return nil
}

func DeleteWorkspaceAgentTx(tx *sql.Tx, workspaceID, name string, cascade bool) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
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
		list := formatReferenceList(refs)
		return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: still referenced by %d binding(s) across %d repo(s) (%s); use cascade to remove them", name, len(refs), len(distinct), list)}
	}
	if refs, err := dispatchesReferencingAgent(tx, workspaceID, name); err != nil {
		return fmt.Errorf("store: delete agent %s: check can_dispatch: %w", name, err)
	} else if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: still referenced by can_dispatch from %s", name, formatReferenceList(refs))}
	}
	var budgets int
	if err := tx.QueryRow("SELECT COUNT(*) FROM token_budgets WHERE workspace_id=? AND agent=?", workspaceID, name).Scan(&budgets); err != nil {
		return fmt.Errorf("store: delete agent %s: check budgets: %w", name, err)
	}
	if budgets > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("store: delete agent %s: still referenced by %d token budget(s)", name, budgets)}
	}
	res, err := tx.Exec("DELETE FROM agents WHERE workspace_id=? AND name=?", workspaceID, name)
	if err != nil {
		return fmt.Errorf("store: delete agent %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return nil
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

func validateAgentScalarRefs(q querier, a fleet.Agent, workspaceID, scopeType string) error {
	var backendExists bool
	if err := q.QueryRow("SELECT EXISTS(SELECT 1 FROM backends WHERE name=?)", a.Backend).Scan(&backendExists); err != nil {
		return fmt.Errorf("store import: check backend %s for agent %s: %w", a.Backend, a.Name, err)
	}
	if !backendExists {
		return &ErrValidation{Msg: fmt.Sprintf("config: agent %q: unknown backend %q", a.Name, a.Backend)}
	}
	switch scopeType {
	case "workspace":
		if a.ScopeRepo != "" {
			return &ErrValidation{Msg: fmt.Sprintf("config: agent %q: scope_repo must be empty for workspace scope", a.Name)}
		}
	case "repo":
		scopeRepo := fleet.NormalizeRepoName(a.ScopeRepo)
		if scopeRepo == "" {
			return &ErrValidation{Msg: fmt.Sprintf("config: agent %q: scope_repo is required for repo scope", a.Name)}
		}
		var repoExists bool
		if err := q.QueryRow("SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)", workspaceID, scopeRepo).Scan(&repoExists); err != nil {
			return fmt.Errorf("store import: check scope_repo %s for agent %s: %w", scopeRepo, a.Name, err)
		}
		if !repoExists {
			return &ErrValidation{Msg: fmt.Sprintf("config: agent %q: scope_repo %q is not a repo in workspace %q", a.Name, a.ScopeRepo, workspaceID)}
		}
	default:
		return &ErrValidation{Msg: fmt.Sprintf("config: agent %q: unsupported scope_type %q", a.Name, a.ScopeType)}
	}
	return nil
}

func replaceAgentSkills(tx *sql.Tx, agentID, agentName string, skills []agentSkillRef) error {
	if _, err := tx.Exec("DELETE FROM agent_skills WHERE agent_id=?", agentID); err != nil {
		return fmt.Errorf("store import: clear agent %s skills: %w", agentName, err)
	}
	for i, skill := range skills {
		if _, err := tx.Exec(
			"INSERT INTO agent_skills(agent_id, skill_id, position) VALUES(?,?,?)",
			agentID, skill.skillID, i,
		); err != nil {
			return fmt.Errorf("store import: insert agent %s skill %s: %w", agentName, skill.skillID, err)
		}
	}
	return nil
}

func replaceAgentDispatches(tx *sql.Tx, agentID, workspaceID, agentName string, targets []string) error {
	if _, err := tx.Exec("DELETE FROM agent_dispatches WHERE source_agent_id=?", agentID); err != nil {
		return fmt.Errorf("store import: clear agent %s can_dispatch: %w", agentName, err)
	}
	for i, target := range targets {
		target = fleet.NormalizeAgentName(target)
		var targetID string
		err := tx.QueryRow("SELECT id FROM agents WHERE workspace_id=? AND name=?", workspaceID, target).Scan(&targetID)
		if errors.Is(err, sql.ErrNoRows) {
			return &ErrValidation{Msg: fmt.Sprintf("store import: agent %s can_dispatch references unknown agent %q", agentName, target)}
		}
		if err != nil {
			return fmt.Errorf("store import: read can_dispatch target %s for agent %s: %w", target, agentName, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO agent_dispatches(source_agent_id, target_agent_id, position) VALUES(?,?,?)",
			agentID, targetID, i,
		); err != nil {
			return fmt.Errorf("store import: insert agent %s can_dispatch %s: %w", agentName, target, err)
		}
	}
	return nil
}

func loadAgentSkillRefs(q querier, agentID string) ([]string, map[string]string, error) {
	rows, err := q.Query(`
		SELECT s.ref
		FROM agent_skills ask
		JOIN skills s ON s.id = ask.skill_id
		WHERE ask.agent_id=?
		ORDER BY ask.position, s.ref`, agentID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		out = append(out, id)
	}
	return out, nil, rows.Err()
}

func loadAgentDispatchRefs(q querier, agentID string) ([]string, error) {
	rows, err := q.Query(`
		SELECT target.name
		FROM agent_dispatches ad
		JOIN agents target ON target.id = ad.target_agent_id
		WHERE ad.source_agent_id=?
		ORDER BY ad.position, target.name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func dispatchesReferencingAgent(q querier, workspaceID, targetName string) ([]string, error) {
	rows, err := q.Query(`
		SELECT source.workspace_id, source.name
		FROM agent_dispatches ad
		JOIN agents source ON source.id = ad.source_agent_id
		JOIN agents target ON target.id = ad.target_agent_id
		WHERE target.workspace_id=? AND target.name=?
		ORDER BY source.workspace_id, source.name`, workspaceID, targetName)
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
