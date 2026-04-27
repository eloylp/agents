// Package fleet implements the agent, skill, and backend HTTP CRUD surface
// plus the methods the MCP fleet-management tools call directly. Wire types,
// validation, and storage paths live together so the REST and MCP surfaces
// stay in lock-step.
//
// The HTTP server constructs a Handler at startup, hands it a
// server.WriteCoordinator that owns the cross-domain CRUD lock and reload
// hook, and mounts the routes via RegisterRoutes. The same Handler satisfies
// the mcp.AgentWriter, mcp.SkillWriter, and mcp.BackendWriter interfaces so
// MCP tools hit the same code path as REST clients.
package fleet

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/mcp"
	"github.com/eloylp/agents/internal/server"
	"github.com/eloylp/agents/internal/store"
)

// Handler implements the /agents, /skills, and /backends HTTP surface plus
// the methods exposed for the MCP agent / skill / backend writers. It also
// owns the /agents/orphans/status endpoint and the read-only fleet snapshot
// view served at GET /agents.
//
// Construct via New and mount with RegisterRoutes. The handler is usable
// without a database — CRUD routes self-skip at registration time in that
// mode while the orphan cache and fleet view continue to serve from the
// in-memory config.
type Handler struct {
	db           *sql.DB                     // optional; CRUD routes self-skip when nil
	coord        server.WriteCoordinator     // required for CRUD writes
	srv          *server.Server              // required — provides Config() snapshot
	provider     server.StatusProvider       // optional; supplies cron schedules to the fleet view
	runtimeState server.RuntimeStateProvider // optional; runtime running/idle status
	logger       zerolog.Logger

	orphanMu    sync.RWMutex
	orphanCache OrphanedAgentsSnapshot
}

// New constructs a Handler. coord and srv are required; db, provider, and
// runtimeState are optional — the handler degrades gracefully when they
// are absent. provider and runtimeState are interfaces so tests can stub
// them without constructing real *autonomous.Scheduler / *observe.Store
// instances; production passes those concrete types directly.
func New(db *sql.DB, coord server.WriteCoordinator, srv *server.Server, provider server.StatusProvider, runtimeState server.RuntimeStateProvider, logger zerolog.Logger) *Handler {
	return &Handler{
		db:           db,
		coord:        coord,
		srv:          srv,
		provider:     provider,
		runtimeState: runtimeState,
		logger:       logger.With().Str("component", "server_fleet").Logger(),
	}
}

// RegisterRoutes mounts the agent, skill, backend, and orphans endpoints on
// r. withTimeout wraps each handler in an http.TimeoutHandler matching the
// daemon's HTTP write-timeout setting.
//
// The orphans status endpoint is mounted unconditionally — it works from the
// in-memory config alone. The agent / skill / backend CRUD routes are
// mounted only when a database was supplied to New; without one those routes
// do not exist on the router.
//
// GET /agents is mounted by the composing server's dispatcher (which also
// handles POST /agents) so both share one mux entry; the dispatcher delegates
// to HandleAgentsView for the read-only fleet snapshot and to
// HandleAgentsCreate for POST.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/agents/orphans/status", withTimeout(http.HandlerFunc(h.HandleOrphansStatus))).Methods(http.MethodGet)

	if h.db == nil {
		return
	}
	r.Handle("/agents/{name}", withTimeout(http.HandlerFunc(h.handleAgent))).Methods(http.MethodGet, http.MethodPatch, http.MethodDelete)

	r.Handle("/skills", withTimeout(http.HandlerFunc(h.handleSkills))).Methods(http.MethodGet, http.MethodPost)
	r.Handle("/skills/{name}", withTimeout(http.HandlerFunc(h.handleSkill))).Methods(http.MethodGet, http.MethodPatch, http.MethodDelete)

	r.Handle("/backends", withTimeout(http.HandlerFunc(h.handleBackends))).Methods(http.MethodGet, http.MethodPost)
	r.Handle("/backends/status", withTimeout(http.HandlerFunc(h.handleBackendsStatus))).Methods(http.MethodGet)
	r.Handle("/backends/discover", withTimeout(http.HandlerFunc(h.handleBackendsDiscover))).Methods(http.MethodPost)
	r.Handle("/backends/local", withTimeout(http.HandlerFunc(h.handleBackendsLocal))).Methods(http.MethodPost)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendGet))).Methods(http.MethodGet)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendPatch))).Methods(http.MethodPatch)
	r.Handle("/backends/{name}", withTimeout(http.HandlerFunc(h.handleBackendDelete))).Methods(http.MethodDelete)
}

// HandleAgentsCreate serves POST /agents. The composing server's /agents
// dispatcher routes POST here and GET to HandleAgentsView so a single mux
// entry serves both the read-only fleet snapshot and CRUD create.
func (h *Handler) HandleAgentsCreate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "store not attached", http.StatusServiceUnavailable)
		return
	}
	var req storeAgentJSON
	if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	canonical, err := h.UpsertAgent(req.toConfig())
	if err != nil {
		h.writeErr(w, err, "agent upsert or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
}

// ── Agent wire types ─────────────────────────────────────────────────────────

type storeAgentJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills"`
	Prompt        string   `json:"prompt"`
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
		Name:          a.Name,
		Backend:       a.Backend,
		Model:         a.Model,
		Skills:        nilSafeStrings(a.Skills),
		Prompt:        a.Prompt,
		AllowPRs:      a.AllowPRs,
		AllowDispatch: a.AllowDispatch,
		CanDispatch:   nilSafeStrings(a.CanDispatch),
		Description:   a.Description,
		AllowMemory:   &allowMem,
	}
}

func (j storeAgentJSON) toConfig() fleet.Agent {
	return fleet.Agent{
		Name:          j.Name,
		Backend:       j.Backend,
		Model:         j.Model,
		Skills:        nilSafeStrings(j.Skills),
		Prompt:        j.Prompt,
		AllowPRs:      j.AllowPRs,
		AllowDispatch: j.AllowDispatch,
		CanDispatch:   nilSafeStrings(j.CanDispatch),
		Description:   j.Description,
		AllowMemory:   j.AllowMemory,
	}
}

// agentPatch is the wire shape for PATCH /agents/{name}. Every field is a
// pointer so clients can distinguish "don't touch" (nil / omitted) from "set
// to zero value" (explicit). Handler merges non-nil fields over the existing
// record, then runs the merged entity through UpsertAgent so the same
// validation and cron-reload paths apply.
type agentPatch struct {
	Backend       *string   `json:"backend,omitempty"`
	Model         *string   `json:"model,omitempty"`
	Skills        *[]string `json:"skills,omitempty"`
	Prompt        *string   `json:"prompt,omitempty"`
	AllowPRs      *bool     `json:"allow_prs,omitempty"`
	AllowDispatch *bool     `json:"allow_dispatch,omitempty"`
	CanDispatch   *[]string `json:"can_dispatch,omitempty"`
	Description   *string   `json:"description,omitempty"`
	AllowMemory   *bool     `json:"allow_memory,omitempty"`
}

func (p agentPatch) anyFieldSet() bool {
	return p.Backend != nil || p.Model != nil || p.Skills != nil || p.Prompt != nil ||
		p.AllowPRs != nil || p.AllowDispatch != nil || p.CanDispatch != nil ||
		p.Description != nil || p.AllowMemory != nil
}

func (p agentPatch) apply(a *fleet.Agent) {
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

// agentPatchFromMCP converts the public mcp.AgentPatch into the internal wire
// shape so the same merge logic serves both REST and MCP clients.
func agentPatchFromMCP(p mcp.AgentPatch) agentPatch {
	return agentPatch{
		Backend:       p.Backend,
		Model:         p.Model,
		Skills:        p.Skills,
		Prompt:        p.Prompt,
		AllowPRs:      p.AllowPRs,
		AllowDispatch: p.AllowDispatch,
		CanDispatch:   p.CanDispatch,
		Description:   p.Description,
		AllowMemory:   p.AllowMemory,
	}
}

// ── Agent handlers ───────────────────────────────────────────────────────────

func (h *Handler) handleAgent(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeAgentName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
		agents, err := store.ReadAgents(h.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read agents: %v", err), http.StatusInternalServerError)
			return
		}
		for _, a := range agents {
			if a.Name == name {
				writeJSON(w, http.StatusOK, agentToStoreJSON(a))
				return
			}
		}
		http.NotFound(w, r)

	case http.MethodPatch:
		h.handleAgentPatch(w, r, name)

	case http.MethodDelete:
		cascade := r.URL.Query().Get("cascade") == "true"
		if err := h.DeleteAgent(name, cascade); err != nil {
			h.writeErr(w, err, "agent delete or cron reload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) handleAgentPatch(w http.ResponseWriter, r *http.Request, name string) {
	var req agentPatch
	if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	canonical, err := h.updateAgent(name, req)
	if err != nil {
		h.writeErr(w, err, "agent patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
}

// ── Agent methods (exposed for MCP) ──────────────────────────────────────────

// UpsertAgent writes a single agent definition into the store and reloads the
// cron scheduler. Returns the canonical (normalized) form that was persisted.
// Empty/whitespace names are rejected as *store.ErrValidation.
func (h *Handler) UpsertAgent(a fleet.Agent) (fleet.Agent, error) {
	if strings.TrimSpace(a.Name) == "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "name is required"}
	}
	err := h.coord.Do(func() error {
		return store.UpsertAgent(h.db, a)
	})
	if err != nil {
		return fleet.Agent{}, err
	}
	fleet.NormalizeAgent(&a)
	return a, nil
}

// UpdateAgentPatch applies a partial patch to the named agent. Returns
// *store.ErrNotFound when the agent does not exist. Implements
// mcp.AgentWriter.
func (h *Handler) UpdateAgentPatch(name string, patch mcp.AgentPatch) (fleet.Agent, error) {
	return h.updateAgent(name, agentPatchFromMCP(patch))
}

func (h *Handler) updateAgent(name string, patch agentPatch) (fleet.Agent, error) {
	normalized := fleet.NormalizeAgentName(name)
	var merged fleet.Agent
	err := h.coord.Do(func() error {
		agents, err := store.ReadAgents(h.db)
		if err != nil {
			return err
		}
		var existing *fleet.Agent
		for i := range agents {
			if agents[i].Name == normalized {
				existing = &agents[i]
				break
			}
		}
		if existing == nil {
			return &store.ErrNotFound{Msg: fmt.Sprintf("agent %q not found", normalized)}
		}
		merged = *existing
		patch.apply(&merged)
		return store.UpsertAgent(h.db, merged)
	})
	if err != nil {
		return fleet.Agent{}, err
	}
	fleet.NormalizeAgent(&merged)
	return merged, nil
}

// DeleteAgent removes an agent from the store and reloads the cron scheduler.
// When cascade is true, repo bindings referencing the agent are also removed;
// otherwise a *store.ErrConflict is returned if any binding still references
// the agent.
func (h *Handler) DeleteAgent(name string, cascade bool) error {
	return h.coord.Do(func() error {
		if cascade {
			return store.DeleteAgentCascade(h.db, name)
		}
		return store.DeleteAgent(h.db, name)
	})
}

// ── Skill wire types ─────────────────────────────────────────────────────────

type storeSkillJSON struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

type skillPatch struct {
	Prompt *string `json:"prompt,omitempty"`
}

func (p skillPatch) anyFieldSet() bool { return p.Prompt != nil }

func (p skillPatch) apply(s *fleet.Skill) {
	if p.Prompt != nil {
		s.Prompt = *p.Prompt
	}
}

func skillPatchFromMCP(p mcp.SkillPatch) skillPatch {
	return skillPatch{Prompt: p.Prompt}
}

// ── Skill handlers ───────────────────────────────────────────────────────────

func (h *Handler) handleSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		skills, err := store.ReadSkills(h.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read skills: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeSkillJSON, 0, len(skills))
		for name, sk := range skills {
			out = append(out, storeSkillJSON{Name: name, Prompt: sk.Prompt})
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeSkillJSON
		if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		name, sk, err := h.UpsertSkill(req.Name, fleet.Skill{Prompt: req.Prompt})
		if err != nil {
			h.writeErr(w, err, "skill upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, storeSkillJSON{Name: name, Prompt: sk.Prompt})
	}
}

func (h *Handler) handleSkill(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeSkillName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
		skills, err := store.ReadSkills(h.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read skills: %v", err), http.StatusInternalServerError)
			return
		}
		sk, ok := skills[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, storeSkillJSON{Name: name, Prompt: sk.Prompt})

	case http.MethodPatch:
		h.handleSkillPatch(w, r, name)

	case http.MethodDelete:
		if err := h.DeleteSkill(name); err != nil {
			h.writeErr(w, err, "skill delete or cron reload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) handleSkillPatch(w http.ResponseWriter, r *http.Request, name string) {
	var req skillPatch
	if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	canonicalName, canonical, err := h.updateSkill(name, req)
	if err != nil {
		h.writeErr(w, err, "skill patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, storeSkillJSON{Name: canonicalName, Prompt: canonical.Prompt})
}

// ── Skill methods (exposed for MCP) ──────────────────────────────────────────

// UpsertSkill writes a single skill into the store and reloads the cron
// scheduler. Returns the canonical (normalized) name and Skill that were
// persisted. Empty/whitespace names are rejected as *store.ErrValidation.
func (h *Handler) UpsertSkill(name string, sk fleet.Skill) (string, fleet.Skill, error) {
	if strings.TrimSpace(name) == "" {
		return "", fleet.Skill{}, &store.ErrValidation{Msg: "name is required"}
	}
	err := h.coord.Do(func() error {
		return store.UpsertSkill(h.db, name, sk)
	})
	if err != nil {
		return "", fleet.Skill{}, err
	}
	fleet.NormalizeSkill(&sk)
	return fleet.NormalizeSkillName(name), sk, nil
}

// UpdateSkillPatch applies a partial patch to the named skill. Returns
// *store.ErrNotFound when the skill does not exist. Implements
// mcp.SkillWriter.
func (h *Handler) UpdateSkillPatch(name string, patch mcp.SkillPatch) (string, fleet.Skill, error) {
	return h.updateSkill(name, skillPatchFromMCP(patch))
}

func (h *Handler) updateSkill(name string, patch skillPatch) (string, fleet.Skill, error) {
	normalized := fleet.NormalizeSkillName(name)
	var existing fleet.Skill
	err := h.coord.Do(func() error {
		skills, err := store.ReadSkills(h.db)
		if err != nil {
			return err
		}
		s, ok := skills[normalized]
		if !ok {
			return &store.ErrNotFound{Msg: fmt.Sprintf("skill %q not found", normalized)}
		}
		patch.apply(&s)
		existing = s
		return store.UpsertSkill(h.db, normalized, s)
	})
	if err != nil {
		return "", fleet.Skill{}, err
	}
	fleet.NormalizeSkill(&existing)
	return normalized, existing, nil
}

// DeleteSkill removes the named skill from the store and reloads the cron
// scheduler. Returns *store.ErrConflict if any agent still references the
// skill.
func (h *Handler) DeleteSkill(name string) error {
	return h.coord.Do(func() error {
		return store.DeleteSkill(h.db, name)
	})
}

// ── Backend wire types ───────────────────────────────────────────────────────

type storeBackendJSON struct {
	Name             string   `json:"name"`
	Command          string   `json:"command"`
	Version          string   `json:"version,omitempty"`
	Models           []string `json:"models,omitempty"`
	Healthy          bool     `json:"healthy"`
	HealthDetail     string   `json:"health_detail,omitempty"`
	LocalModelURL    string   `json:"local_model_url,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	MaxPromptChars   int      `json:"max_prompt_chars"`
	RedactionSaltEnv string   `json:"redaction_salt_env"`
}

type localBackendRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type backendPatchJSON struct {
	Command          *string   `json:"command,omitempty"`
	Version          *string   `json:"version,omitempty"`
	Models           *[]string `json:"models,omitempty"`
	Healthy          *bool     `json:"healthy,omitempty"`
	HealthDetail     *string   `json:"health_detail,omitempty"`
	LocalModelURL    *string   `json:"local_model_url,omitempty"`
	TimeoutSeconds   *int      `json:"timeout_seconds,omitempty"`
	MaxPromptChars   *int      `json:"max_prompt_chars,omitempty"`
	RedactionSaltEnv *string   `json:"redaction_salt_env,omitempty"`
}

func (p backendPatchJSON) anyFieldSet() bool {
	return p.Command != nil || p.Version != nil || p.Models != nil || p.Healthy != nil ||
		p.HealthDetail != nil || p.LocalModelURL != nil || p.TimeoutSeconds != nil ||
		p.MaxPromptChars != nil || p.RedactionSaltEnv != nil
}

func (p backendPatchJSON) apply(b *fleet.Backend) {
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
	if p.RedactionSaltEnv != nil {
		b.RedactionSaltEnv = *p.RedactionSaltEnv
	}
}

func backendPatchFromMCP(p mcp.BackendPatch) backendPatchJSON {
	return backendPatchJSON{
		Command:          p.Command,
		Version:          p.Version,
		Models:           p.Models,
		Healthy:          p.Healthy,
		HealthDetail:     p.HealthDetail,
		LocalModelURL:    p.LocalModelURL,
		TimeoutSeconds:   p.TimeoutSeconds,
		MaxPromptChars:   p.MaxPromptChars,
		RedactionSaltEnv: p.RedactionSaltEnv,
	}
}

func backendToStoreJSON(name string, b fleet.Backend) storeBackendJSON {
	return storeBackendJSON{
		Name:             name,
		Command:          b.Command,
		Version:          b.Version,
		Models:           nilSafeStrings(b.Models),
		Healthy:          b.Healthy,
		HealthDetail:     b.HealthDetail,
		LocalModelURL:    b.LocalModelURL,
		TimeoutSeconds:   b.TimeoutSeconds,
		MaxPromptChars:   b.MaxPromptChars,
		RedactionSaltEnv: b.RedactionSaltEnv,
	}
}

func (j storeBackendJSON) toConfig() fleet.Backend {
	return fleet.Backend{
		Command:          j.Command,
		Version:          j.Version,
		Models:           nilSafeStrings(j.Models),
		Healthy:          j.Healthy,
		HealthDetail:     j.HealthDetail,
		LocalModelURL:    j.LocalModelURL,
		TimeoutSeconds:   j.TimeoutSeconds,
		MaxPromptChars:   j.MaxPromptChars,
		RedactionSaltEnv: j.RedactionSaltEnv,
	}
}

// ── Backend handlers ─────────────────────────────────────────────────────────

func (h *Handler) handleBackends(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		all, err := store.ReadBackends(h.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeBackendJSON, 0, len(all))
		for name, b := range all {
			out = append(out, backendToStoreJSON(name, b))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeBackendJSON
		if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		name, b, err := h.UpsertBackend(req.Name, req.toConfig())
		if err != nil {
			h.writeErr(w, err, "backend upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
	}
}

func (h *Handler) handleBackendsStatus(w http.ResponseWriter, r *http.Request) {
	existing, err := store.ReadBackends(h.db)
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	diag := backends.RunDiagnostics(r.Context(), existing)
	writeJSON(w, http.StatusOK, diag)
}

func (h *Handler) handleBackendsDiscover(w http.ResponseWriter, r *http.Request) {
	var diag any
	err := h.coord.Do(func() error {
		d, derr := backends.DiscoverAndPersist(r.Context(), h.db)
		if derr != nil {
			return derr
		}
		diag = d
		return nil
	})
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
	if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
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

	var stored fleet.Backend
	err := h.coord.Do(func() error {
		existing, err := store.ReadBackends(h.db)
		if err != nil {
			return err
		}
		base, ok := existing[backends.ClaudeName]
		if !ok || strings.TrimSpace(base.Command) == "" {
			return &store.ErrValidation{Msg: "claude backend must be discovered first"}
		}
		if current, ok := existing[name]; ok && strings.TrimSpace(current.LocalModelURL) == "" {
			return &store.ErrConflict{Msg: "name already exists and is not a local backend"}
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

		if err := store.UpsertBackend(h.db, name, local); err != nil {
			return err
		}
		stored = local
		return nil
	})
	if err != nil {
		status := storeErrStatus(err)
		h.logger.Error().Err(err).Msg("local backend upsert failed")
		http.Error(w, fmt.Sprintf("local backend upsert or cron reload: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, stored))
}

func backendPathName(r *http.Request) string {
	return fleet.NormalizeBackendName(mux.Vars(r)["name"])
}

func (h *Handler) handleBackendGet(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	all, err := store.ReadBackends(h.db)
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
	var req backendPatchJSON
	if !decodeBody(w, r, h.srv.Config().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
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

// ── Backend methods (exposed for MCP) ────────────────────────────────────────

// UpsertBackend writes a single backend definition into the store and reloads
// the cron scheduler. Returns the canonical (normalized) name and config that
// were persisted. Empty/whitespace names are rejected as *store.ErrValidation.
func (h *Handler) UpsertBackend(name string, b fleet.Backend) (string, fleet.Backend, error) {
	if strings.TrimSpace(name) == "" {
		return "", fleet.Backend{}, &store.ErrValidation{Msg: "name is required"}
	}
	err := h.coord.Do(func() error {
		return store.UpsertBackend(h.db, name, b)
	})
	if err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&b)
	fleet.ApplyBackendDefaults(&b)
	return fleet.NormalizeBackendName(name), b, nil
}

// UpdateBackendPatch applies a partial patch to the named backend. Returns
// *store.ErrNotFound when the backend does not exist. Implements
// mcp.BackendWriter.
func (h *Handler) UpdateBackendPatch(name string, patch mcp.BackendPatch) (string, fleet.Backend, error) {
	return h.updateBackend(name, backendPatchFromMCP(patch))
}

func (h *Handler) updateBackend(name string, patch backendPatchJSON) (string, fleet.Backend, error) {
	normalized := fleet.NormalizeBackendName(name)
	var existing fleet.Backend
	err := h.coord.Do(func() error {
		all, err := store.ReadBackends(h.db)
		if err != nil {
			return err
		}
		b, ok := all[normalized]
		if !ok {
			return &store.ErrNotFound{Msg: fmt.Sprintf("backend %q not found", normalized)}
		}
		patch.apply(&b)
		existing = b
		return store.UpsertBackend(h.db, normalized, b)
	})
	if err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&existing)
	fleet.ApplyBackendDefaults(&existing)
	return normalized, existing, nil
}

// DeleteBackend removes the named backend from the store and reloads the cron
// scheduler. Returns *store.ErrConflict if any agent still references the
// backend.
func (h *Handler) DeleteBackend(name string) error {
	return h.coord.Do(func() error {
		return store.DeleteBackend(h.db, name)
	})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

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
