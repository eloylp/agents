// Package fleet implements the agent, skill, and backend HTTP CRUD surface
// plus the methods the MCP fleet-management tools call directly. Wire types,
// validation, and storage paths live together so the REST and MCP surfaces
// stay in lock-step.
//
// The handler reads from SQLite on every request and writes through the
// store package's per-call transactions. The /agents snapshot view also
// pulls scheduling and runtime status from the scheduler and the observe
// store; those are concrete pointers because the daemon ships as a single
// composed binary.
package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/store"
)

// Handler implements the /agents, /skills, and /backends HTTP surface plus
// the methods exposed for the MCP agent / skill / backend writers. It also
// owns the /agents/orphans/status endpoint and the read-only fleet snapshot
// view served at GET /agents.
type Handler struct {
	store        *store.Store
	maxBodyBytes int64
	sched        *scheduler.Scheduler // schedule + last-run state for the /agents view
	obs          *observe.Store       // running/idle state for the /agents view
	logger       zerolog.Logger
}

// New constructs a Handler.
func New(st *store.Store, maxBodyBytes int64, sched *scheduler.Scheduler, obs *observe.Store, logger zerolog.Logger) *Handler {
	return &Handler{
		store:        st,
		maxBodyBytes: maxBodyBytes,
		sched:        sched,
		obs:          obs,
		logger:       logger.With().Str("component", "server_fleet").Logger(),
	}
}

// RegisterRoutes mounts the agent, skill, backend, and orphans endpoints
// on r. withTimeout wraps each handler in an http.TimeoutHandler matching
// the daemon's HTTP write-timeout setting.
//
// GET /agents and POST /agents are mounted by the composing daemon so the
// top-level route can combine the read-only fleet snapshot with CRUD create.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/agents/orphans/status", withTimeout(http.HandlerFunc(h.HandleOrphansStatus))).Methods(http.MethodGet)
	r.Handle("/agents/{name}", withTimeout(http.HandlerFunc(h.handleAgentGet))).Methods(http.MethodGet)
	r.Handle("/agents/{name}", withTimeout(http.HandlerFunc(h.handleAgentPatchByName))).Methods(http.MethodPatch)
	r.Handle("/agents/{name}", withTimeout(http.HandlerFunc(h.handleAgentDelete))).Methods(http.MethodDelete)
	r.Handle("/graph/layout", withTimeout(http.HandlerFunc(h.handleGraphLayoutGet))).Methods(http.MethodGet)
	r.Handle("/graph/layout", withTimeout(http.HandlerFunc(h.handleGraphLayoutPut))).Methods(http.MethodPut)
	r.Handle("/graph/layout", withTimeout(http.HandlerFunc(h.handleGraphLayoutDelete))).Methods(http.MethodDelete)

	r.Handle("/workspaces", withTimeout(http.HandlerFunc(h.handleWorkspacesList))).Methods(http.MethodGet)
	r.Handle("/workspaces", withTimeout(http.HandlerFunc(h.handleWorkspaceCreate))).Methods(http.MethodPost)
	r.Handle("/workspaces/{workspace}", withTimeout(http.HandlerFunc(h.handleWorkspaceGet))).Methods(http.MethodGet)
	r.Handle("/workspaces/{workspace}", withTimeout(http.HandlerFunc(h.handleWorkspacePatch))).Methods(http.MethodPatch)
	r.Handle("/workspaces/{workspace}", withTimeout(http.HandlerFunc(h.handleWorkspaceDelete))).Methods(http.MethodDelete)
	r.Handle("/workspaces/{workspace}/guardrails", withTimeout(http.HandlerFunc(h.handleWorkspaceGuardrailsGet))).Methods(http.MethodGet)
	r.Handle("/workspaces/{workspace}/guardrails", withTimeout(http.HandlerFunc(h.handleWorkspaceGuardrailsPut))).Methods(http.MethodPut)

	r.Handle("/skills", withTimeout(http.HandlerFunc(h.handleSkillsList))).Methods(http.MethodGet)
	r.Handle("/skills", withTimeout(http.HandlerFunc(h.handleSkillCreate))).Methods(http.MethodPost)
	r.Handle("/skills/{id}", withTimeout(http.HandlerFunc(h.handleSkillGet))).Methods(http.MethodGet)
	r.Handle("/skills/{id}", withTimeout(http.HandlerFunc(h.handleSkillPatchByName))).Methods(http.MethodPatch)
	r.Handle("/skills/{id}", withTimeout(http.HandlerFunc(h.handleSkillDelete))).Methods(http.MethodDelete)

	r.Handle("/guardrails", withTimeout(http.HandlerFunc(h.handleGuardrailsList))).Methods(http.MethodGet)
	r.Handle("/guardrails", withTimeout(http.HandlerFunc(h.handleGuardrailCreate))).Methods(http.MethodPost)
	r.Handle("/guardrails/{id}", withTimeout(http.HandlerFunc(h.handleGuardrailGet))).Methods(http.MethodGet)
	r.Handle("/guardrails/{id}", withTimeout(http.HandlerFunc(h.handleGuardrailPatchByName))).Methods(http.MethodPatch)
	r.Handle("/guardrails/{id}", withTimeout(http.HandlerFunc(h.handleGuardrailDelete))).Methods(http.MethodDelete)
	r.Handle("/guardrails/{id}/reset", withTimeout(http.HandlerFunc(h.handleGuardrailReset))).Methods(http.MethodPost)

	r.Handle("/prompts", withTimeout(http.HandlerFunc(h.handlePromptsList))).Methods(http.MethodGet)
	r.Handle("/prompts", withTimeout(http.HandlerFunc(h.handlePromptCreate))).Methods(http.MethodPost)
	r.Handle("/prompts/{id}", withTimeout(http.HandlerFunc(h.handlePromptGet))).Methods(http.MethodGet)
	r.Handle("/prompts/{id}", withTimeout(http.HandlerFunc(h.handlePromptPatchByID))).Methods(http.MethodPatch)
	r.Handle("/prompts/{id}", withTimeout(http.HandlerFunc(h.handlePromptDelete))).Methods(http.MethodDelete)

	r.Handle("/backends", withTimeout(http.HandlerFunc(h.handleBackendsList))).Methods(http.MethodGet)
	r.Handle("/backends", withTimeout(http.HandlerFunc(h.handleBackendCreate))).Methods(http.MethodPost)
	r.Handle("/backends/status", withTimeout(http.HandlerFunc(h.handleBackendsStatus))).Methods(http.MethodGet)
	r.Handle("/backends/discover", withTimeout(http.HandlerFunc(h.handleBackendsDiscover))).Methods(http.MethodPost)
	r.Handle("/backends/local", withTimeout(http.HandlerFunc(h.handleBackendsLocal))).Methods(http.MethodPost)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendGet))).Methods(http.MethodGet)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendPatch))).Methods(http.MethodPatch)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendDelete))).Methods(http.MethodDelete)
}

// HandleAgentsCreate serves POST /agents. The composing daemon's /agents
// dispatcher routes POST here and GET to HandleAgentsView so a single mux
// entry serves both the read-only fleet snapshot and CRUD create.
func (h *Handler) HandleAgentsCreate(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		http.Error(w, "store not attached", http.StatusServiceUnavailable)
		return
	}
	var req storeAgentJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if req.WorkspaceID == "" {
		req.WorkspaceID = fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	}
	canonical, err := h.UpsertAgent(req.toConfig())
	if err != nil {
		h.writeErr(w, err, "agent upsert or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
}

// ── Agent wire types ────────────────────────────────────────────────────────────────────────────────────

type storeAgentJSON struct {
	ID            string   `json:"id,omitempty"`
	WorkspaceID   string   `json:"workspace_id,omitempty"`
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills"`
	Prompt        string   `json:"prompt,omitempty"`
	PromptID      string   `json:"prompt_id,omitempty"`
	PromptRef     string   `json:"prompt_ref,omitempty"`
	ScopeType     string   `json:"scope_type,omitempty"`
	ScopeRepo     string   `json:"scope_repo,omitempty"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	CanDispatch   []string `json:"can_dispatch"`
	Description   string   `json:"description"`
	// AllowMemory is a *bool so POST clients that omit the field get the
	// default-true semantics (`Agent.AllowMemory == nil` → IsAllowMemory()
	// returns true). Responses always populate it (see agentToStoreJSON) so
	// every read sees a concrete value.
	AllowMemory *bool `json:"allow_memory,omitempty"`
}

func agentToStoreJSON(a fleet.Agent) storeAgentJSON {
	allowMem := a.IsAllowMemory()
	return storeAgentJSON{
		ID:            a.ID,
		WorkspaceID:   a.WorkspaceID,
		Name:          a.Name,
		Backend:       a.Backend,
		Model:         a.Model,
		Skills:        nilSafeStrings(a.Skills),
		PromptID:      a.PromptID,
		PromptRef:     a.PromptRef,
		ScopeType:     a.ScopeType,
		ScopeRepo:     a.ScopeRepo,
		AllowPRs:      a.AllowPRs,
		AllowDispatch: a.AllowDispatch,
		CanDispatch:   nilSafeStrings(a.CanDispatch),
		Description:   a.Description,
		AllowMemory:   &allowMem,
	}
}

func (j storeAgentJSON) toConfig() fleet.Agent {
	return fleet.Agent{
		WorkspaceID:   j.WorkspaceID,
		Name:          j.Name,
		Backend:       j.Backend,
		Model:         j.Model,
		Skills:        nilSafeStrings(j.Skills),
		Prompt:        j.Prompt,
		PromptID:      j.PromptID,
		PromptRef:     j.PromptRef,
		ScopeType:     j.ScopeType,
		ScopeRepo:     j.ScopeRepo,
		AllowPRs:      j.AllowPRs,
		AllowDispatch: j.AllowDispatch,
		CanDispatch:   nilSafeStrings(j.CanDispatch),
		Description:   j.Description,
		AllowMemory:   j.AllowMemory,
	}
}

// AgentPatch is the partial-update shape for an agent. Every field is a
// pointer so callers (REST PATCH /agents/{name} and the MCP update_agent
// tool) can distinguish "don't touch" (nil / omitted) from "set to zero
// value" (explicit). Handler merges non-nil fields over the existing
// record, then runs the merged entity through UpsertAgent so the same
// validation and cron-reload paths apply.
type AgentPatch struct {
	WorkspaceID   *string   `json:"workspace_id,omitempty"`
	Backend       *string   `json:"backend,omitempty"`
	Model         *string   `json:"model,omitempty"`
	Skills        *[]string `json:"skills,omitempty"`
	Prompt        *string   `json:"prompt,omitempty"`
	PromptID      *string   `json:"prompt_id,omitempty"`
	PromptRef     *string   `json:"prompt_ref,omitempty"`
	ScopeType     *string   `json:"scope_type,omitempty"`
	ScopeRepo     *string   `json:"scope_repo,omitempty"`
	AllowPRs      *bool     `json:"allow_prs,omitempty"`
	AllowDispatch *bool     `json:"allow_dispatch,omitempty"`
	CanDispatch   *[]string `json:"can_dispatch,omitempty"`
	Description   *string   `json:"description,omitempty"`
	AllowMemory   *bool     `json:"allow_memory,omitempty"`
}

// AnyFieldSet reports whether at least one patch field is non-nil. Used by
// both the REST PATCH handler and the MCP update_agent tool to reject empty
// payloads before hitting the store.
func (p AgentPatch) AnyFieldSet() bool {
	return p.WorkspaceID != nil || p.Backend != nil || p.Model != nil || p.Skills != nil || p.Prompt != nil || p.PromptID != nil ||
		p.PromptRef != nil || p.ScopeType != nil || p.ScopeRepo != nil ||
		p.AllowPRs != nil || p.AllowDispatch != nil || p.CanDispatch != nil ||
		p.Description != nil || p.AllowMemory != nil
}

func (p AgentPatch) apply(a *fleet.Agent) {
	if p.WorkspaceID != nil {
		a.WorkspaceID = *p.WorkspaceID
	}
	if p.Backend != nil {
		a.Backend = *p.Backend
	}
	if p.Model != nil {
		a.Model = *p.Model
	}
	if p.Skills != nil {
		a.Skills = *p.Skills
	}
	if p.Prompt != nil {
		a.Prompt = *p.Prompt
	}
	if p.PromptID != nil {
		a.PromptID = *p.PromptID
		if strings.TrimSpace(*p.PromptID) != "" {
			a.PromptRef = ""
		}
	}
	if p.PromptRef != nil {
		a.PromptRef = *p.PromptRef
		if strings.TrimSpace(*p.PromptRef) != "" {
			a.PromptID = ""
		}
	}
	if p.ScopeType != nil {
		a.ScopeType = *p.ScopeType
	}
	if p.ScopeRepo != nil {
		a.ScopeRepo = *p.ScopeRepo
	}
	if p.AllowPRs != nil {
		a.AllowPRs = *p.AllowPRs
	}
	if p.AllowDispatch != nil {
		a.AllowDispatch = *p.AllowDispatch
	}
	if p.CanDispatch != nil {
		a.CanDispatch = *p.CanDispatch
	}
	if p.Description != nil {
		a.Description = *p.Description
	}
	if p.AllowMemory != nil {
		v := *p.AllowMemory
		a.AllowMemory = &v
	}
}

// ── Agent handlers ────────────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleAgentGet(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeAgentName(mux.Vars(r)["name"])
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	agents, err := h.store.ReadAgents()
	if err != nil {
		http.Error(w, fmt.Sprintf("read agents: %v", err), http.StatusInternalServerError)
		return
	}
	idx := slices.IndexFunc(agents, func(a fleet.Agent) bool {
		return a.Name == name && fleet.NormalizeWorkspaceID(a.WorkspaceID) == workspaceID
	})
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(agents[idx]))
}

func (h *Handler) handleAgentPatchByName(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeAgentName(mux.Vars(r)["name"])
	h.handleAgentPatch(w, r, name)
}

func (h *Handler) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeAgentName(mux.Vars(r)["name"])
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	cascade := r.URL.Query().Get("cascade") == "true"
	if err := h.DeleteAgentInWorkspace(workspaceID, name, cascade); err != nil {
		h.writeErr(w, err, "agent delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleAgentPatch(w http.ResponseWriter, r *http.Request, name string) {
	var req AgentPatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.AnyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	if req.Prompt != nil {
		http.Error(w, "agent prompt bodies are import-only; use prompt_ref", http.StatusBadRequest)
		return
	}
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	canonical, err := h.updateAgent(name, workspaceID, req)
	if err != nil {
		h.writeErr(w, err, "agent patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
}

// ── Agent methods (exposed for MCP) ───────────────────────────────────────────────────────────────────────────────

// UpsertAgent writes a single agent definition into the store and reloads the
// cron scheduler. Returns the canonical (normalized) form that was persisted.
// Empty/whitespace names are rejected as *store.ErrValidation.
func (h *Handler) UpsertAgent(a fleet.Agent) (fleet.Agent, error) {
	if strings.TrimSpace(a.Name) == "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "name is required"}
	}
	if strings.TrimSpace(a.Prompt) != "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "agent prompt bodies are import-only; use prompt_ref"}
	}
	if strings.TrimSpace(a.PromptID) != "" && strings.TrimSpace(a.PromptRef) != "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "prompt_id and prompt_ref are mutually exclusive"}
	}
	if strings.TrimSpace(a.PromptRef) == "" && strings.TrimSpace(a.PromptID) == "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "prompt_ref is required"}
	}
	normalizedName := fleet.NormalizeAgentName(a.Name)
	if err := h.store.UpsertAgent(a); err != nil {
		return fleet.Agent{}, err
	}
	agents, err := h.store.ReadAgents()
	if err != nil {
		return fleet.Agent{}, err
	}
	workspaceID := fleet.NormalizeWorkspaceID(a.WorkspaceID)
	idx := slices.IndexFunc(agents, func(agent fleet.Agent) bool {
		return agent.Name == normalizedName && fleet.NormalizeWorkspaceID(agent.WorkspaceID) == workspaceID
	})
	if idx < 0 {
		return fleet.Agent{}, fmt.Errorf("agent %q not found after upsert", normalizedName)
	}
	return agents[idx], nil
}

// UpdateAgentPatch applies a partial patch to the named agent. Returns
// *store.ErrNotFound when the agent does not exist. Used by both the REST
// PATCH handler and the MCP update_agent tool.
func (h *Handler) UpdateAgentPatch(name string, patch AgentPatch) (fleet.Agent, error) {
	return h.UpdateAgentPatchInWorkspace(fleet.DefaultWorkspaceID, name, patch)
}

// UpdateAgentPatchInWorkspace applies a partial patch to the named agent in
// workspace. Empty workspace keeps the default-workspace compatibility path.
func (h *Handler) UpdateAgentPatchInWorkspace(workspaceID, name string, patch AgentPatch) (fleet.Agent, error) {
	return h.updateAgent(name, workspaceID, patch)
}

func (h *Handler) updateAgent(name, workspaceID string, patch AgentPatch) (fleet.Agent, error) {
	if patch.Prompt != nil {
		return fleet.Agent{}, &store.ErrValidation{Msg: "agent prompt bodies are import-only; use prompt_ref"}
	}
	if patch.PromptID != nil && patch.PromptRef != nil &&
		strings.TrimSpace(*patch.PromptID) != "" && strings.TrimSpace(*patch.PromptRef) != "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "prompt_id and prompt_ref are mutually exclusive"}
	}
	normalized := fleet.NormalizeAgentName(name)
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	agents, err := h.store.ReadAgents()
	if err != nil {
		return fleet.Agent{}, err
	}
	idx := slices.IndexFunc(agents, func(a fleet.Agent) bool {
		return a.Name == normalized && fleet.NormalizeWorkspaceID(a.WorkspaceID) == workspaceID
	})
	if idx < 0 {
		return fleet.Agent{}, &store.ErrNotFound{Msg: fmt.Sprintf("agent %q not found in workspace %q", normalized, workspaceID)}
	}
	merged := agents[idx]
	if patch.WorkspaceID != nil && fleet.NormalizeWorkspaceID(*patch.WorkspaceID) != fleet.NormalizeWorkspaceID(merged.WorkspaceID) {
		return fleet.Agent{}, &store.ErrValidation{Msg: "workspace_id cannot be changed with PATCH; create the agent in the target workspace instead"}
	}
	patch.apply(&merged)
	if err := h.store.UpsertAgent(merged); err != nil {
		return fleet.Agent{}, err
	}
	fleet.NormalizeAgent(&merged)
	return merged, nil
}

// DeleteAgent removes an agent from the store. When cascade is true, repo
// bindings referencing the agent are also removed; otherwise
// *store.ErrConflict is returned if any binding still references the
// agent.
func (h *Handler) DeleteAgent(name string, cascade bool) error {
	return h.DeleteAgentInWorkspace(fleet.DefaultWorkspaceID, name, cascade)
}

func (h *Handler) DeleteAgentInWorkspace(workspaceID, name string, cascade bool) error {
	if cascade {
		return h.store.DeleteWorkspaceAgentCascade(workspaceID, name)
	}
	return h.store.DeleteWorkspaceAgent(workspaceID, name)
}

// ── Skill wire types ────────────────────────────────────────────────────────────────────────────────────

type storeSkillJSON struct {
	ID          string `json:"id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Name        string `json:"name"`
	Prompt      string `json:"prompt"`
}

func skillToStoreJSON(id string, sk fleet.Skill) storeSkillJSON {
	return storeSkillJSON{
		ID:          id,
		WorkspaceID: sk.WorkspaceID,
		Repo:        sk.Repo,
		Name:        sk.Name,
		Prompt:      sk.Prompt,
	}
}

// SkillPatch is the partial-update shape for a skill. Used by both the REST
// PATCH /skills/{id} handler and the MCP update_skill tool. A nil Prompt means
// "don't touch".
type SkillPatch struct {
	Prompt *string `json:"prompt,omitempty"`
}

// AnyFieldSet reports whether at least one patch field is non-nil. Used by
// both the REST PATCH handler and the MCP update_skill tool to reject empty
// payloads before hitting the store.
func (p SkillPatch) AnyFieldSet() bool { return p.Prompt != nil }

func (p SkillPatch) apply(s *fleet.Skill) {
	if p.Prompt != nil {
		s.Prompt = *p.Prompt
	}
}

// ── Skill handlers ────────────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleSkillsList(w http.ResponseWriter, _ *http.Request) {
	skills, err := h.store.ReadSkills()
	if err != nil {
		http.Error(w, fmt.Sprintf("read skills: %v", err), http.StatusInternalServerError)
		return
	}
	out := make([]storeSkillJSON, 0, len(skills))
	for id, sk := range skills {
		out = append(out, skillToStoreJSON(id, sk))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleSkillCreate(w http.ResponseWriter, r *http.Request) {
	var req storeSkillJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	id := req.ID
	if id == "" && req.WorkspaceID == "" && req.Repo == "" {
		id = req.Name
	}
	name, sk, err := h.UpsertSkill(id, fleet.Skill{ID: id, WorkspaceID: req.WorkspaceID, Repo: req.Repo, Name: req.Name, Prompt: req.Prompt})
	if err != nil {
		h.writeErr(w, err, "skill upsert or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, skillToStoreJSON(name, sk))
}

func (h *Handler) handleSkillGet(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeSkillName(mux.Vars(r)["id"])
	skills, err := h.store.ReadSkills()
	if err != nil {
		http.Error(w, fmt.Sprintf("read skills: %v", err), http.StatusInternalServerError)
		return
	}
	sk, ok := skills[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, skillToStoreJSON(name, sk))
}

func (h *Handler) handleSkillPatchByName(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeSkillName(mux.Vars(r)["id"])
	h.handleSkillPatch(w, r, name)
}

func (h *Handler) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeSkillName(mux.Vars(r)["id"])
	if err := h.DeleteSkill(name); err != nil {
		h.writeErr(w, err, "skill delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleSkillPatch(w http.ResponseWriter, r *http.Request, name string) {
	var req SkillPatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.AnyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	canonicalName, canonical, err := h.updateSkill(name, req)
	if err != nil {
		h.writeErr(w, err, "skill patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, skillToStoreJSON(canonicalName, canonical))
}

// ── Skill methods (exposed for MCP) ───────────────────────────────────────────────────────────────────────────────

// UpsertSkill writes a single skill into the store and reloads the cron
// scheduler. Returns the canonical stable id and Skill that were persisted.
// Empty id is accepted when the Skill carries a name, allowing scoped skill
// creates to derive the same stable id shape as imports.
func (h *Handler) UpsertSkill(name string, sk fleet.Skill) (string, fleet.Skill, error) {
	if strings.TrimSpace(name) == "" && strings.TrimSpace(sk.Name) == "" {
		return "", fleet.Skill{}, &store.ErrValidation{Msg: "name is required"}
	}
	if err := h.store.UpsertSkill(name, sk); err != nil {
		return "", fleet.Skill{}, err
	}
	skills, err := h.store.ReadSkills()
	if err != nil {
		return "", fleet.Skill{}, err
	}
	normalizedID := fleet.NormalizeSkillName(name)
	if normalizedID != "" {
		if persisted, ok := skills[normalizedID]; ok {
			return normalizedID, persisted, nil
		}
	}
	fleet.NormalizeSkill(&sk)
	for id, persisted := range skills {
		if persisted.WorkspaceID == sk.WorkspaceID && persisted.Repo == sk.Repo && persisted.Name == sk.Name {
			return id, persisted, nil
		}
	}
	return "", fleet.Skill{}, &store.ErrNotFound{Msg: fmt.Sprintf("skill %q not found after upsert", sk.Name)}
}

// UpdateSkillPatch applies a partial patch to the named skill. Returns
// *store.ErrNotFound when the skill does not exist. Used by both the REST
// PATCH handler and the MCP update_skill tool.
func (h *Handler) UpdateSkillPatch(name string, patch SkillPatch) (string, fleet.Skill, error) {
	return h.updateSkill(name, patch)
}

func (h *Handler) updateSkill(name string, patch SkillPatch) (string, fleet.Skill, error) {
	normalized := fleet.NormalizeSkillName(name)
	skills, err := h.store.ReadSkills()
	if err != nil {
		return "", fleet.Skill{}, err
	}
	existing, ok := skills[normalized]
	if !ok {
		return "", fleet.Skill{}, &store.ErrNotFound{Msg: fmt.Sprintf("skill %q not found", normalized)}
	}
	patch.apply(&existing)
	if err := h.store.UpsertSkill(normalized, existing); err != nil {
		return "", fleet.Skill{}, err
	}
	fleet.NormalizeSkill(&existing)
	return normalized, existing, nil
}

// DeleteSkill removes the named skill from the store. Returns
// *store.ErrConflict if any agent still references the skill.
func (h *Handler) DeleteSkill(name string) error {
	return h.store.DeleteSkill(name)
}

// ── Prompt wire types ────────────────────────────────────────────────────────────────────────────────────

type storePromptJSON struct {
	ID          string `json:"id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
}

type PromptPatch struct {
	Description *string `json:"description,omitempty"`
	Content     *string `json:"content,omitempty"`
}

func (p PromptPatch) AnyFieldSet() bool { return p.Description != nil || p.Content != nil }

func (p PromptPatch) apply(prompt *fleet.Prompt) {
	if p.Description != nil {
		prompt.Description = *p.Description
	}
	if p.Content != nil {
		prompt.Content = *p.Content
	}
}

func promptToStoreJSON(p fleet.Prompt) storePromptJSON {
	return storePromptJSON{
		ID:          p.ID,
		WorkspaceID: p.WorkspaceID,
		Repo:        p.Repo,
		Name:        p.Name,
		Description: p.Description,
		Content:     p.Content,
	}
}

func (j storePromptJSON) toConfig() fleet.Prompt {
	return fleet.Prompt{
		ID:          j.ID,
		WorkspaceID: j.WorkspaceID,
		Repo:        j.Repo,
		Name:        j.Name,
		Description: j.Description,
		Content:     j.Content,
	}
}

func (h *Handler) handlePromptsList(w http.ResponseWriter, _ *http.Request) {
	prompts, err := h.store.ReadPrompts()
	if err != nil {
		http.Error(w, fmt.Sprintf("read prompts: %v", err), http.StatusInternalServerError)
		return
	}
	out := make([]storePromptJSON, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, promptToStoreJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handlePromptCreate(w http.ResponseWriter, r *http.Request) {
	var req storePromptJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	prompt, err := h.UpsertPrompt(req.toConfig())
	if err != nil {
		h.writeErr(w, err, "prompt upsert")
		return
	}
	writeJSON(w, http.StatusOK, promptToStoreJSON(prompt))
}

func (h *Handler) handlePromptGet(w http.ResponseWriter, r *http.Request) {
	ref := mux.Vars(r)["id"]
	prompt, err := h.store.ReadPrompt(ref)
	if err != nil {
		h.writeErr(w, err, "prompt get")
		return
	}
	writeJSON(w, http.StatusOK, promptToStoreJSON(prompt))
}

func (h *Handler) handlePromptPatchByID(w http.ResponseWriter, r *http.Request) {
	ref := mux.Vars(r)["id"]
	var req PromptPatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.AnyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	prompt, err := h.updatePrompt(ref, req)
	if err != nil {
		h.writeErr(w, err, "prompt patch")
		return
	}
	writeJSON(w, http.StatusOK, promptToStoreJSON(prompt))
}

func (h *Handler) handlePromptDelete(w http.ResponseWriter, r *http.Request) {
	ref := mux.Vars(r)["id"]
	if err := h.DeletePrompt(ref); err != nil {
		h.writeErr(w, err, "prompt delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UpsertPrompt(p fleet.Prompt) (fleet.Prompt, error) {
	return h.store.UpsertPrompt(p)
}

func (h *Handler) UpdatePromptPatch(ref string, patch PromptPatch) (fleet.Prompt, error) {
	return h.updatePrompt(ref, patch)
}

func (h *Handler) updatePrompt(ref string, patch PromptPatch) (fleet.Prompt, error) {
	prompt, err := h.store.ReadPrompt(ref)
	if err != nil {
		return fleet.Prompt{}, err
	}
	merged := prompt
	patch.apply(&merged)
	return h.store.UpsertPrompt(merged)
}

func (h *Handler) DeletePrompt(ref string) error {
	return h.store.DeletePrompt(ref)
}

// ── Backend wire types ──────────────────────────────────────────────────────────────────────────────────

type storeBackendJSON struct {
	Name           string   `json:"name"`
	Command        string   `json:"command"`
	Version        string   `json:"version,omitempty"`
	Models         []string `json:"models,omitempty"`
	Healthy        bool     `json:"healthy"`
	HealthDetail   string   `json:"health_detail,omitempty"`
	LocalModelURL  string   `json:"local_model_url,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxPromptChars int      `json:"max_prompt_chars"`
}

type localBackendRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// BackendPatch is the partial-update shape for a backend. Used by both the
// REST PATCH /backends/{name} handler and the MCP update_backend tool. Every
// field is a pointer so callers can bump a single setting (e.g.
// timeout_seconds) without resubmitting the rest.
type BackendPatch struct {
	Command        *string   `json:"command,omitempty"`
	Version        *string   `json:"version,omitempty"`
	Models         *[]string `json:"models,omitempty"`
	Healthy        *bool     `json:"healthy,omitempty"`
	HealthDetail   *string   `json:"health_detail,omitempty"`
	LocalModelURL  *string   `json:"local_model_url,omitempty"`
	TimeoutSeconds *int      `json:"timeout_seconds,omitempty"`
	MaxPromptChars *int      `json:"max_prompt_chars,omitempty"`
}

// AnyFieldSet reports whether at least one patch field is non-nil. Used by
// both the REST PATCH handler and the MCP update_backend tool to reject empty
// payloads before hitting the store.
func (p BackendPatch) AnyFieldSet() bool {
	return p.Command != nil || p.Version != nil || p.Models != nil || p.Healthy != nil ||
		p.HealthDetail != nil || p.LocalModelURL != nil || p.TimeoutSeconds != nil ||
		p.MaxPromptChars != nil
}

func (p BackendPatch) apply(b *fleet.Backend) {
	if p.Command != nil {
		b.Command = *p.Command
	}
	if p.Version != nil {
		b.Version = *p.Version
	}
	if p.Models != nil {
		b.Models = *p.Models
	}
	if p.Healthy != nil {
		b.Healthy = *p.Healthy
	}
	if p.HealthDetail != nil {
		b.HealthDetail = *p.HealthDetail
	}
	if p.LocalModelURL != nil {
		b.LocalModelURL = *p.LocalModelURL
	}
	if p.TimeoutSeconds != nil {
		b.TimeoutSeconds = *p.TimeoutSeconds
	}
	if p.MaxPromptChars != nil {
		b.MaxPromptChars = *p.MaxPromptChars
	}
}

func backendToStoreJSON(name string, b fleet.Backend) storeBackendJSON {
	return storeBackendJSON{
		Name:           name,
		Command:        b.Command,
		Version:        b.Version,
		Models:         nilSafeStrings(b.Models),
		Healthy:        b.Healthy,
		HealthDetail:   b.HealthDetail,
		LocalModelURL:  b.LocalModelURL,
		TimeoutSeconds: b.TimeoutSeconds,
		MaxPromptChars: b.MaxPromptChars,
	}
}

func (j storeBackendJSON) toConfig() fleet.Backend {
	return fleet.Backend{
		Command:        j.Command,
		Version:        j.Version,
		Models:         nilSafeStrings(j.Models),
		Healthy:        j.Healthy,
		HealthDetail:   j.HealthDetail,
		LocalModelURL:  j.LocalModelURL,
		TimeoutSeconds: j.TimeoutSeconds,
		MaxPromptChars: j.MaxPromptChars,
	}
}

// ── Backend handlers ────────────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleBackendsList(w http.ResponseWriter, _ *http.Request) {
	all, err := h.store.ReadBackends()
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	out := make([]storeBackendJSON, 0, len(all))
	for name, b := range all {
		out = append(out, backendToStoreJSON(name, b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleBackendCreate(w http.ResponseWriter, r *http.Request) {
	var req storeBackendJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	name, b, err := h.UpsertBackend(req.Name, req.toConfig())
	if err != nil {
		h.writeErr(w, err, "backend upsert or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
}

func (h *Handler) handleBackendsStatus(w http.ResponseWriter, r *http.Request) {
	existing, err := h.store.ReadBackends()
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	diag := backends.RunDiagnostics(r.Context(), existing)
	writeJSON(w, http.StatusOK, diag)
}

func (h *Handler) handleBackendsDiscover(w http.ResponseWriter, r *http.Request) {
	diag, err := backends.DiscoverAndPersist(r.Context(), h.store)
	if err != nil {
		status := storeErrStatus(err)
		h.logger.Error().Err(err).Msg("backend discovery failed")
		http.Error(w, fmt.Sprintf("backend discovery: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, diag)
}

func (h *Handler) handleBackendsLocal(w http.ResponseWriter, r *http.Request) {
	var req localBackendRequest
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	name := fleet.NormalizeBackendName(req.Name)
	if name == "" {
		name = backends.ClaudeLocalName
	}
	if name == backends.ClaudeName || name == backends.CodexName {
		http.Error(w, "name is reserved for built-in backends", http.StatusBadRequest)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if _, err := url.ParseRequestURI(req.URL); err != nil {
		http.Error(w, fmt.Sprintf("invalid url: %v", err), http.StatusBadRequest)
		return
	}

	existing, err := h.store.ReadBackends()
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	base, ok := existing[backends.ClaudeName]
	if !ok || strings.TrimSpace(base.Command) == "" {
		http.Error(w, "claude backend must be discovered first", http.StatusBadRequest)
		return
	}
	if current, ok := existing[name]; ok && strings.TrimSpace(current.LocalModelURL) == "" {
		http.Error(w, "name already exists and is not a local backend", http.StatusConflict)
		return
	}

	local := existing[name]
	local.Command = base.Command
	local.LocalModelURL = req.URL

	diagMap := map[string]fleet.Backend{
		backends.ClaudeName: base,
		name:                local,
	}
	diag := backends.RunDiagnostics(r.Context(), diagMap)
	for _, b := range diag.Backends {
		if b.Name != name {
			continue
		}
		local.Version = b.Version
		local.Models = b.Models
		local.Healthy = b.Healthy
		local.HealthDetail = b.HealthDetail
		local.Command = b.Command
		local.LocalModelURL = b.LocalModelURL
		break
	}

	if err := h.store.UpsertBackend(name, local); err != nil {
		status := storeErrStatus(err)
		h.logger.Error().Err(err).Msg("local backend upsert failed")
		http.Error(w, fmt.Sprintf("local backend upsert: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, local))
}

func backendPathName(r *http.Request) string {
	return fleet.NormalizeBackendName(mux.Vars(r)["name"])
}

func (h *Handler) handleBackendGet(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	all, err := h.store.ReadBackends()
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	b, ok := all[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
}

func (h *Handler) handleBackendPatch(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	var req BackendPatch
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if !req.AnyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	if req.TimeoutSeconds != nil && *req.TimeoutSeconds <= 0 {
		http.Error(w, "timeout_seconds must be positive", http.StatusBadRequest)
		return
	}
	if req.MaxPromptChars != nil && *req.MaxPromptChars <= 0 {
		http.Error(w, "max_prompt_chars must be positive", http.StatusBadRequest)
		return
	}
	canonicalName, canonical, err := h.updateBackend(name, req)
	if err != nil {
		h.writeErr(w, err, "backend patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(canonicalName, canonical))
}

func (h *Handler) handleBackendDelete(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	if err := h.DeleteBackend(name); err != nil {
		h.writeErr(w, err, "backend delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Backend methods (exposed for MCP) ────────────────────────────────────────────────────────────────────────────

// UpsertBackend writes a single backend definition into the store and reloads
// the cron scheduler. Returns the canonical (normalized) name and config that
// were persisted. Empty/whitespace names are rejected as *store.ErrValidation.
func (h *Handler) UpsertBackend(name string, b fleet.Backend) (string, fleet.Backend, error) {
	if strings.TrimSpace(name) == "" {
		return "", fleet.Backend{}, &store.ErrValidation{Msg: "name is required"}
	}
	if err := h.store.UpsertBackend(name, b); err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&b)
	fleet.ApplyBackendDefaults(&b)
	return fleet.NormalizeBackendName(name), b, nil
}

// UpdateBackendPatch applies a partial patch to the named backend. Returns
// *store.ErrNotFound when the backend does not exist. Used by both the REST
// PATCH handler and the MCP update_backend tool.
func (h *Handler) UpdateBackendPatch(name string, patch BackendPatch) (string, fleet.Backend, error) {
	return h.updateBackend(name, patch)
}

func (h *Handler) updateBackend(name string, patch BackendPatch) (string, fleet.Backend, error) {
	normalized := fleet.NormalizeBackendName(name)
	all, err := h.store.ReadBackends()
	if err != nil {
		return "", fleet.Backend{}, err
	}
	existing, ok := all[normalized]
	if !ok {
		return "", fleet.Backend{}, &store.ErrNotFound{Msg: fmt.Sprintf("backend %q not found", normalized)}
	}
	patch.apply(&existing)
	if err := h.store.UpsertBackend(normalized, existing); err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&existing)
	fleet.ApplyBackendDefaults(&existing)
	return normalized, existing, nil
}

// DeleteBackend removes the named backend from the store. Returns
// *store.ErrConflict if any agent still references the backend.
func (h *Handler) DeleteBackend(name string) error {
	return h.store.DeleteBackend(name)
}

// ── Helpers ────────────────────────────────────────────────────────────────────────────────────────────

func (h *Handler) writeErr(w http.ResponseWriter, err error, op string) {
	h.logger.Error().Err(err).Msgf("store crud: %s failed", op)
	http.Error(w, fmt.Sprintf("%s: %v", op, err), storeErrStatus(err))
}

func storeErrStatus(err error) int {
	var v *store.ErrValidation
	if errors.As(err, &v) {
		return http.StatusBadRequest
	}
	var n *store.ErrNotFound
	if errors.As(err, &n) {
		return http.StatusNotFound
	}
	var c *store.ErrConflict
	if errors.As(err, &c) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request, limit int64, out *T) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func nilSafeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
