package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importAgents(tx *sql.Tx, agents []fleet.Agent) error {
	for _, a := range agents {
		workspaceID := fleet.NormalizeWorkspaceID(a.WorkspaceID)
		scopeType := a.ScopeType
		if scopeType == "" {
			scopeType = "workspace"
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
		skills, err := json.Marshal(skillRefs)
		if err != nil {
			return fmt.Errorf("store import: marshal agent %s skills: %w", a.Name, err)
		}
		canDispatch, err := json.Marshal(a.CanDispatch)
		if err != nil {
			return fmt.Errorf("store import: marshal agent %s can_dispatch: %w", a.Name, err)
		}
		allowPRs := boolToInt(a.AllowPRs)
		allowDispatch := boolToInt(a.AllowDispatch)
		allowMemory := bindingEnabledInt(a.AllowMemory)
		if _, err := tx.Exec(`
			INSERT INTO agents
			  (id,workspace_id,name,backend,model,skills,prompt_id,scope_type,scope_repo,allow_prs,allow_dispatch,can_dispatch,description,allow_memory)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id, name) DO UPDATE SET
				id = excluded.id,
				backend = excluded.backend,
				model = excluded.model,
				skills = excluded.skills,
				prompt_id = excluded.prompt_id,
				scope_type = excluded.scope_type,
				scope_repo = excluded.scope_repo,
				allow_prs = excluded.allow_prs,
				allow_dispatch = excluded.allow_dispatch,
				can_dispatch = excluded.can_dispatch,
				description = excluded.description,
				allow_memory = excluded.allow_memory`,
			id, workspaceID, a.Name, a.Backend, a.Model, string(skills), promptID, scopeType, a.ScopeRepo,
			allowPRs, allowDispatch, string(canDispatch), a.Description, allowMemory,
		); err != nil {
			return fmt.Errorf("store import: upsert agent %s: %w", a.Name, err)
		}
	}
	return nil
}

func resolveAgentSkillRefs(tx *sql.Tx, a fleet.Agent, workspaceID, scopeType string) ([]string, error) {
	repo := ""
	if scopeType == "repo" {
		repo = a.ScopeRepo
	}
	refs := make([]string, 0, len(a.Skills))
	for _, raw := range a.Skills {
		ref := fleet.NormalizeSkillName(raw)
		id, err := resolveVisibleCatalogRef(tx, "skills", ref, workspaceID, repo)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if skill, ok, readErr := readSkillScopeByID(tx, ref); readErr != nil {
					return nil, fmt.Errorf("store import: read skill %s for agent %s: %w", ref, a.Name, readErr)
				} else if ok {
					if skill.Repo != "" && repo == "" {
						return nil, fmt.Errorf("workspace-scoped agent %q references repo-scoped skill %q without repo context", a.Name, ref)
					}
					return nil, fmt.Errorf("agent %q references skill %q outside its visible catalog scope", a.Name, ref)
				}
				return nil, fmt.Errorf("store import: agent %s references unknown skill %q", a.Name, ref)
			}
			return nil, fmt.Errorf("store import: resolve skill %s for agent %s: %w", ref, a.Name, err)
		}
		refs = append(refs, id)
	}
	return refs, nil
}

func ensureAgentPrompt(tx *sql.Tx, a fleet.Agent) (string, error) {
	if a.PromptID != "" {
		scopeRepo := ""
		if a.ScopeType == "repo" {
			scopeRepo = a.ScopeRepo
		}
		if _, err := resolveVisibleCatalogID(tx, "prompts", a.PromptID, a.WorkspaceID, scopeRepo); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("store import: agent %s references unknown prompt_id %q", a.Name, a.PromptID)
			}
			return "", fmt.Errorf("store import: read prompt_id %s for agent %s: %w", a.PromptID, a.Name, err)
		}
		return a.PromptID, nil
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
		return "", fmt.Errorf("store import: agent %s references unknown prompt_ref %q", a.Name, a.PromptRef)
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
		SELECT a.id,a.workspace_id,a.name,a.backend,a.model,a.skills,a.prompt_id,COALESCE(p.name, ''),COALESCE(p.workspace_id, ''),COALESCE(p.repo, ''),a.scope_type,a.scope_repo,a.allow_prs,a.allow_dispatch,a.can_dispatch,a.description,a.allow_memory
		FROM agents a
		LEFT JOIN prompts p ON p.id = a.prompt_id
		ORDER BY a.name`)
	if err != nil {
		return fmt.Errorf("store load: query agents: %w", err)
	}
	defer rows.Close()

	var agents []fleet.Agent
	for rows.Next() {
		var id, workspaceID, name, backend, model, skillsJSON, promptID, promptRef, promptWorkspace, promptRepo, scopeType, scopeRepo, canDispatchJSON, description string
		var allowPRs, allowDispatch, allowMemory int
		if err := rows.Scan(
			&id, &workspaceID, &name, &backend, &model, &skillsJSON, &promptID, &promptRef, &promptWorkspace, &promptRepo, &scopeType, &scopeRepo,
			&allowPRs, &allowDispatch, &canDispatchJSON, &description, &allowMemory,
		); err != nil {
			return fmt.Errorf("store load: scan agent: %w", err)
		}
		var skills []string
		if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
			return fmt.Errorf("store load: parse agent %s skills: %w", name, err)
		}
		var canDispatch []string
		if err := json.Unmarshal([]byte(canDispatchJSON), &canDispatch); err != nil {
			return fmt.Errorf("store load: parse agent %s can_dispatch: %w", name, err)
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
			Skills:        skills,
			PromptID:      promptID,
			PromptRef:     promptRef,
			PromptScope:   promptScope,
			ScopeType:     scopeType,
			ScopeRepo:     scopeRepo,
			AllowPRs:      intToBool(allowPRs),
			AllowDispatch: intToBool(allowDispatch),
			CanDispatch:   canDispatch,
			Description:   description,
			AllowMemory:   &allowMem,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate agents: %w", err)
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
