package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// ── JSON wire types ───────────────────────────────────────────────────────────

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

// storeAgentPatchJSON is the wire shape for PATCH /agents/{name}. Every field
// is a pointer so clients can distinguish "don't touch" (nil / omitted) from
// "set to zero value" (explicit). Handler merges non-nil fields over the
// existing record, then runs the merged entity through UpsertAgent so the
// same validation and cron-reload paths apply.
type storeAgentPatchJSON struct {
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

// anyFieldSet reports whether the patch carries any field to apply. An empty
// PATCH body is rejected so callers don't accidentally no-op a write.
func (p storeAgentPatchJSON) anyFieldSet() bool {
	return p.Backend != nil || p.Model != nil || p.Skills != nil || p.Prompt != nil ||
		p.AllowPRs != nil || p.AllowDispatch != nil || p.CanDispatch != nil ||
		p.Description != nil || p.AllowMemory != nil
}

// apply mutates a in place with any non-nil field on p. Name is preserved
// because it is addressed via the URL path and is not patchable.
func (p storeAgentPatchJSON) apply(a *fleet.Agent) {
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

type storeSkillJSON struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// storeSkillPatchJSON is the wire shape for PATCH /skills/{name}. Skills only
// have Prompt as a mutable field today but the shape leaves room for future
// additions without a breaking client rewrite.
type storeSkillPatchJSON struct {
	Prompt *string `json:"prompt,omitempty"`
}

func (p storeSkillPatchJSON) anyFieldSet() bool { return p.Prompt != nil }

func (p storeSkillPatchJSON) apply(s *fleet.Skill) {
	if p.Prompt != nil {
		s.Prompt = *p.Prompt
	}
}

type storeBackendJSON struct {
	Name             string            `json:"name"`
	Command          string            `json:"command"`
	Version          string            `json:"version,omitempty"`
	Models           []string          `json:"models,omitempty"`
	Healthy          bool              `json:"healthy"`
	HealthDetail     string            `json:"health_detail,omitempty"`
	LocalModelURL    string            `json:"local_model_url,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MaxPromptChars   int               `json:"max_prompt_chars"`
	RedactionSaltEnv string            `json:"redaction_salt_env"`
}

type localBackendRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// storeBackendPatchJSON is the wire shape for PATCH /backends/{name}. Every
// field is a pointer so clients can update a single setting (e.g. bump
// timeout_seconds) without resubmitting the rest. Supersedes the legacy
// backendRuntimeSettingsJSON shape, which covered only the two runtime
// knobs — the handler accepts both shapes for backwards compatibility.
type storeBackendPatchJSON struct {
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

func (p storeBackendPatchJSON) anyFieldSet() bool {
	return p.Command != nil || p.Version != nil || p.Models != nil || p.Healthy != nil ||
		p.HealthDetail != nil || p.LocalModelURL != nil || p.TimeoutSeconds != nil ||
		p.MaxPromptChars != nil || p.RedactionSaltEnv != nil
}

func (p storeBackendPatchJSON) apply(b *fleet.Backend) {
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

// backendRuntimeSettingsJSON is the legacy wire shape for PATCH
// /backends/{name}, restricted to the two runtime-tunable fields. The handler
// still accepts this shape; new clients should prefer storeBackendPatchJSON,
// which covers every backend field.
type backendRuntimeSettingsJSON struct {
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
	MaxPromptChars *int `json:"max_prompt_chars,omitempty"`
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

type storeBindingJSON struct {
	ID      int64    `json:"id,omitempty"`
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Events  []string `json:"events,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

func bindingToStoreJSON(b fleet.Binding) storeBindingJSON {
	enabled := b.IsEnabled()
	return storeBindingJSON{
		ID:      b.ID,
		Agent:   b.Agent,
		Labels:  b.Labels,
		Events:  b.Events,
		Cron:    b.Cron,
		Enabled: &enabled,
	}
}

func (j storeBindingJSON) toConfig() fleet.Binding {
	return fleet.Binding{
		ID:      j.ID,
		Agent:   j.Agent,
		Labels:  j.Labels,
		Events:  j.Events,
		Cron:    j.Cron,
		Enabled: j.Enabled,
	}
}

type storeRepoJSON struct {
	Name     string             `json:"name"`
	Enabled  bool               `json:"enabled"`
	Bindings []storeBindingJSON `json:"bindings"`
}

func repoToStoreJSON(r fleet.Repo) storeRepoJSON {
	bindings := make([]storeBindingJSON, len(r.Use))
	for i, b := range r.Use {
		bindings[i] = bindingToStoreJSON(b)
	}
	return storeRepoJSON{Name: r.Name, Enabled: r.Enabled, Bindings: bindings}
}

func (j storeRepoJSON) toConfig() fleet.Repo {
	use := make([]fleet.Binding, len(j.Bindings))
	for i, b := range j.Bindings {
		use[i] = b.toConfig()
	}
	return fleet.Repo{Name: j.Name, Enabled: j.Enabled, Use: use}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func nilSafeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody reads the full request body (enforcing the byte limit via
// http.MaxBytesReader so that trailing garbage beyond a valid JSON value is
// also accounted for), then JSON-unmarshals it into out. It returns false and
// writes an appropriate HTTP error on any failure.
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

// storeErrStatus maps store mutation errors to the appropriate HTTP status
// code. ErrValidation (bad field values) → 400, ErrConflict (invariant
// violations, referenced-by failures) → 409, ErrNotFound → 404,
// anything else → 500.
func storeErrStatus(err error) int {
	var valErr *store.ErrValidation
	if errors.As(err, &valErr) {
		return http.StatusBadRequest
	}
	var conflictErr *store.ErrConflict
	if errors.As(err, &conflictErr) {
		return http.StatusConflict
	}
	var notFoundErr *store.ErrNotFound
	if errors.As(err, &notFoundErr) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// storeWriteErr maps an error from a store write operation to an HTTP response
// and a structured log entry. op identifies the failing operation (e.g.
// "agent upsert or cron reload") and appears in both the log line and the
// HTTP error body so callers and operators see the same context.
func (s *Server) storeWriteErr(w http.ResponseWriter, err error, op string) {
	s.logger.Error().Err(err).Msgf("store crud: %s failed", op)
	http.Error(w, fmt.Sprintf("%s: %v", op, err), storeErrStatus(err))
}

// reloadCron re-reads the full config from the DB as a consistent snapshot
// and calls Reload on the attached CronReloader (if any). All four entity
// types are read within a single transaction so a concurrent /api/store write
// cannot produce a mixed-epoch snapshot.
//
// MUST be called with s.storeMu held so that no other write can commit and
// re-read a newer snapshot between the point this read begins and the point
// Reload applies the result. Without the lock the "DB commit + snapshot read +
// Reload" sequence is not monotonic: a slow caller can overwrite a newer
// in-memory state that a concurrent faster caller already applied.
func (s *Server) reloadCron() error {
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return fmt.Errorf("read config snapshot for cron reload: %w", err)
	}

	// Reload the scheduler/engine first. If Reload fails we must not update
	// the server's routing config — doing so would leave the daemon split across
	// two config epochs (server on the new snapshot, scheduler/engine on the
	// old one) until the next successful reload or restart.
	if s.cronReloader != nil {
		if err := s.cronReloader.Reload(repos, agents, skills, backends); err != nil {
			return err
		}
	}

	// Reload succeeded: update the server's in-memory routing config so that
	// webhook event handlers (/webhooks/github, /agents/run) and read APIs
	// (/api/agents, /api/config) reflect the post-write state immediately
	// without a restart. Copy-on-write: build a new config value from the
	// current snapshot, replacing only the four CRUD-mutable fields.
	// Daemon-level config (HTTP, proxy, log) is never changed by CRUD writes
	// and is preserved unchanged.
	s.cfgMu.Lock()
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends
	s.cfg = &newCfg
	s.cfgMu.Unlock()
	s.refreshOrphanedAgents(&newCfg)

	return nil
}

// ── /api/store/agents ────────────────────────────────────────────────────────

// handleStoreAgents serves GET and POST /api/store/agents.
func (s *Server) handleStoreAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agents, err := store.ReadAgents(s.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read agents: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeAgentJSON, len(agents))
		for i, a := range agents {
			out[i] = agentToStoreJSON(a)
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeAgentJSON
		if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		canonical, err := s.UpsertAgent(req.toConfig())
		if err != nil {
			s.storeWriteErr(w, err, "agent upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
	}
}

// UpsertAgent writes a single agent definition into the store and reloads the
// cron scheduler. Returns the canonical (normalized) form that was persisted
// so callers can surface the same shape REST clients see in the POST response
// — lowercase name, trimmed prompt, etc.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors via storeErrStatus, mirroring the
// behaviour of POST /agents.
//
// Exposed so non-HTTP surfaces (e.g. the MCP create_agent tool) can run the
// same upsert path as POST /agents without going through the router.
func (s *Server) UpsertAgent(a fleet.Agent) (fleet.Agent, error) {
	if strings.TrimSpace(a.Name) == "" {
		return fleet.Agent{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertAgent(s.db, a)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return fleet.Agent{}, err
	}
	fleet.NormalizeAgent(&a)
	return a, nil
}

// handleStoreAgent serves GET, PATCH, and DELETE /api/store/agents/{name}.
func (s *Server) handleStoreAgent(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeAgentName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
		agents, err := store.ReadAgents(s.db)
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
		s.handleUpdateAgent(w, r, name)

	case http.MethodDelete:
		cascade := r.URL.Query().Get("cascade") == "true"
		if err := s.DeleteAgent(name, cascade); err != nil {
			s.storeWriteErr(w, err, "agent delete or cron reload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleUpdateAgent serves PATCH /agents/{name}. It decodes a
// storeAgentPatchJSON, merges non-nil fields over the existing agent, and runs
// the result through UpdateAgent so the same validation and cron-reload
// path as POST /agents applies.
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request, name string) {
	var req storeAgentPatchJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	canonical, err := s.UpdateAgent(name, req)
	if err != nil {
		s.storeWriteErr(w, err, "agent patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, agentToStoreJSON(canonical))
}

// UpdateAgent applies a partial patch to the named agent and writes the merged
// record back through UpsertAgent. Returns *store.ErrNotFound when the agent
// does not exist so the REST handler can map to 404 / MCP can surface a user
// error. The merge+upsert runs under storeMu to stay consistent with the
// rest of the CRUD surface.
//
// Exposed so non-HTTP surfaces (e.g. the MCP update_agent tool) can drive the
// same path as PATCH /agents/{name} without going through the router.
func (s *Server) UpdateAgent(name string, patch storeAgentPatchJSON) (fleet.Agent, error) {
	normalized := fleet.NormalizeAgentName(name)
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	agents, err := store.ReadAgents(s.db)
	if err != nil {
		return fleet.Agent{}, err
	}
	var existing *fleet.Agent
	for i := range agents {
		if agents[i].Name == normalized {
			existing = &agents[i]
			break
		}
	}
	if existing == nil {
		return fleet.Agent{}, &store.ErrNotFound{Msg: fmt.Sprintf("agent %q not found", normalized)}
	}
	merged := *existing
	patch.apply(&merged)
	if err := store.UpsertAgent(s.db, merged); err != nil {
		return fleet.Agent{}, err
	}
	if err := s.reloadCron(); err != nil {
		return fleet.Agent{}, err
	}
	fleet.NormalizeAgent(&merged)
	return merged, nil
}

// DeleteAgent removes an agent from the store and reloads the cron scheduler.
// When cascade is true, repo bindings referencing the agent are also removed;
// otherwise a *store.ErrConflict is returned if any binding still references
// the agent (HTTP 409 / MCP user error via storeErrStatus).
//
// Exposed so non-HTTP surfaces (e.g. the MCP delete_agent tool) can run the
// same delete path as DELETE /agents/{name} without going through the router.
func (s *Server) DeleteAgent(name string, cascade bool) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	var err error
	if cascade {
		err = store.DeleteAgentCascade(s.db, name)
	} else {
		err = store.DeleteAgent(s.db, name)
	}
	if err != nil {
		return err
	}
	return s.reloadCron()
}

// ── /api/store/skills ─────────────────────────────────────────────────────────

// handleStoreSkills serves GET and POST /api/store/skills.
func (s *Server) handleStoreSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		skills, err := store.ReadSkills(s.db)
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
		if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		name, sk, err := s.UpsertSkill(req.Name, fleet.Skill{Prompt: req.Prompt})
		if err != nil {
			s.storeWriteErr(w, err, "skill upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, storeSkillJSON{Name: name, Prompt: sk.Prompt})
	}
}

// UpsertSkill writes a single skill into the store and reloads the cron
// scheduler. Returns the canonical (normalized) name and Skill that were
// persisted so callers can surface the same shape REST clients see in the
// POST response — lowercase/trimmed name, trimmed prompt.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors via storeErrStatus, mirroring the
// behaviour of UpsertAgent and POST /skills.
//
// Exposed so non-HTTP surfaces (e.g. the MCP create_skill tool) can run the
// same upsert path as POST /skills without going through the router.
func (s *Server) UpsertSkill(name string, sk fleet.Skill) (string, fleet.Skill, error) {
	if strings.TrimSpace(name) == "" {
		return "", fleet.Skill{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertSkill(s.db, name, sk)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return "", fleet.Skill{}, err
	}
	fleet.NormalizeSkill(&sk)
	return fleet.NormalizeSkillName(name), sk, nil
}

// handleStoreSkill serves GET, PATCH, and DELETE /api/store/skills/{name}.
func (s *Server) handleStoreSkill(w http.ResponseWriter, r *http.Request) {
	name := fleet.NormalizeSkillName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
		skills, err := store.ReadSkills(s.db)
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
		s.handleUpdateSkill(w, r, name)

	case http.MethodDelete:
		if err := s.DeleteSkill(name); err != nil {
			s.storeWriteErr(w, err, "skill delete or cron reload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleUpdateSkill serves PATCH /skills/{name}. Decodes a storeSkillPatchJSON,
// merges non-nil fields over the existing skill, and runs the result through
// UpdateSkill so the same validation and cron-reload path as POST /skills
// applies.
func (s *Server) handleUpdateSkill(w http.ResponseWriter, r *http.Request, name string) {
	var req storeSkillPatchJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if !req.anyFieldSet() {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	canonicalName, canonical, err := s.UpdateSkill(name, req)
	if err != nil {
		s.storeWriteErr(w, err, "skill patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, storeSkillJSON{Name: canonicalName, Prompt: canonical.Prompt})
}

// UpdateSkill applies a partial patch to the named skill and writes the merged
// record back through UpsertSkill. Returns *store.ErrNotFound when the skill
// does not exist. The merge+upsert runs under storeMu to stay consistent
// with the rest of the CRUD surface.
//
// Exposed so non-HTTP surfaces (e.g. the MCP update_skill tool) can drive the
// same path as PATCH /skills/{name} without going through the router.
func (s *Server) UpdateSkill(name string, patch storeSkillPatchJSON) (string, fleet.Skill, error) {
	normalized := fleet.NormalizeSkillName(name)
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	skills, err := store.ReadSkills(s.db)
	if err != nil {
		return "", fleet.Skill{}, err
	}
	existing, ok := skills[normalized]
	if !ok {
		return "", fleet.Skill{}, &store.ErrNotFound{Msg: fmt.Sprintf("skill %q not found", normalized)}
	}
	patch.apply(&existing)
	if err := store.UpsertSkill(s.db, normalized, existing); err != nil {
		return "", fleet.Skill{}, err
	}
	if err := s.reloadCron(); err != nil {
		return "", fleet.Skill{}, err
	}
	fleet.NormalizeSkill(&existing)
	return normalized, existing, nil
}

// DeleteSkill removes the named skill from the store and reloads the cron
// scheduler. Returns *store.ErrConflict if any agent still references the
// skill (HTTP 409 / MCP user error via storeErrStatus).
//
// Exposed so non-HTTP surfaces (e.g. the MCP delete_skill tool) can run the
// same delete path as DELETE /skills/{name} without going through the router.
func (s *Server) DeleteSkill(name string) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if err := store.DeleteSkill(s.db, name); err != nil {
		return err
	}
	return s.reloadCron()
}

// ── /api/store/backends ───────────────────────────────────────────────────────

// handleStoreBackends serves GET and POST /api/store/backends.
func (s *Server) handleStoreBackends(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		backends, err := store.ReadBackends(s.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeBackendJSON, 0, len(backends))
		for name, b := range backends {
			out = append(out, backendToStoreJSON(name, b))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeBackendJSON
		if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		name, b, err := s.UpsertBackend(req.Name, req.toConfig())
		if err != nil {
			s.storeWriteErr(w, err, "backend upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
	}
}

// UpsertBackend writes a single backend definition into the store and reloads
// the cron scheduler. Returns the canonical (normalized) name and config that
// were persisted so callers can surface the same shape REST clients see in the
// POST /backends response — lowercase name, trimmed command, defaults applied.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors via storeErrStatus, mirroring the
// behaviour of UpsertAgent / UpsertSkill.
//
// Exposed so non-HTTP surfaces (e.g. the MCP create_backend tool) can run the
// same upsert path as POST /backends without going through the router.
func (s *Server) UpsertBackend(name string, b fleet.Backend) (string, fleet.Backend, error) {
	if strings.TrimSpace(name) == "" {
		return "", fleet.Backend{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertBackend(s.db, name, b)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&b)
	fleet.ApplyBackendDefaults(&b)
	return fleet.NormalizeBackendName(name), b, nil
}

// handleBackendsStatus serves GET /backends/status with live diagnostics.
func (s *Server) handleBackendsStatus(w http.ResponseWriter, r *http.Request) {
	existing, err := store.ReadBackends(s.db)
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	diag := backends.RunDiagnostics(r.Context(), existing)
	writeJSON(w, http.StatusOK, diag)
}

// handleBackendsDiscover serves POST /backends/discover. It reruns discovery,
// persists backend metadata, and hot-reloads the in-memory config.
func (s *Server) handleBackendsDiscover(w http.ResponseWriter, r *http.Request) {
	s.storeMu.Lock()
	diag, err := backends.DiscoverAndPersist(r.Context(), s.db)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		status := storeErrStatus(err)
		s.logger.Error().Err(err).Msg("backend discovery failed")
		http.Error(w, fmt.Sprintf("backend discovery: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, diag)
}

// handleBackendsLocal serves POST /backends/local. It creates or updates a
// named local backend by wiring the discovered Claude CLI to a local
// OpenAI-compatible URL via ANTHROPIC_BASE_URL.
func (s *Server) handleBackendsLocal(w http.ResponseWriter, r *http.Request) {
	var req localBackendRequest
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
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

	s.storeMu.Lock()
	existing, err := store.ReadBackends(s.db)
	if err != nil {
		s.storeMu.Unlock()
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	base, ok := existing[backends.ClaudeName]
	if !ok || strings.TrimSpace(base.Command) == "" {
		s.storeMu.Unlock()
		http.Error(w, "claude backend must be discovered first", http.StatusBadRequest)
		return
	}

	if current, ok := existing[name]; ok && strings.TrimSpace(current.LocalModelURL) == "" {
		s.storeMu.Unlock()
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

	err = store.UpsertBackend(s.db, name, local)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		status := storeErrStatus(err)
		s.logger.Error().Err(err).Msg("local backend upsert failed")
		http.Error(w, fmt.Sprintf("local backend upsert or cron reload: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, local))
}

func backendPathName(r *http.Request) string {
	name := fleet.NormalizeBackendName(mux.Vars(r)["name"])
	return name
}

// handleStoreBackendGet serves GET /api/store/backends/{name}.
func (s *Server) handleStoreBackendGet(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	backends, err := store.ReadBackends(s.db)
	if err != nil {
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	b, ok := backends[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
}

// handleStoreBackendPatch serves PATCH /api/store/backends/{name}. Accepts the
// full storeBackendPatchJSON field set; the legacy backendRuntimeSettingsJSON
// shape remains wire-compatible because it is a proper subset.
func (s *Server) handleStoreBackendPatch(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	var req storeBackendPatchJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
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
	canonicalName, canonical, err := s.UpdateBackend(name, req)
	if err != nil {
		s.storeWriteErr(w, err, "backend patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(canonicalName, canonical))
}

// UpdateBackend applies a partial patch to the named backend and writes the
// merged record back through store.UpsertBackend. Returns *store.ErrNotFound
// when the backend does not exist. Runs under storeMu so it stays consistent
// with the rest of the CRUD surface.
//
// Exposed so non-HTTP surfaces (e.g. the MCP update_backend tool) can drive
// the same path as PATCH /backends/{name} without going through the router.
func (s *Server) UpdateBackend(name string, patch storeBackendPatchJSON) (string, fleet.Backend, error) {
	normalized := fleet.NormalizeBackendName(name)
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	backendsByName, err := store.ReadBackends(s.db)
	if err != nil {
		return "", fleet.Backend{}, err
	}
	existing, ok := backendsByName[normalized]
	if !ok {
		return "", fleet.Backend{}, &store.ErrNotFound{Msg: fmt.Sprintf("backend %q not found", normalized)}
	}
	patch.apply(&existing)
	if err := store.UpsertBackend(s.db, normalized, existing); err != nil {
		return "", fleet.Backend{}, err
	}
	if err := s.reloadCron(); err != nil {
		return "", fleet.Backend{}, err
	}
	fleet.NormalizeBackend(&existing)
	fleet.ApplyBackendDefaults(&existing)
	return normalized, existing, nil
}

// handleStoreBackendDelete serves DELETE /api/store/backends/{name}.
func (s *Server) handleStoreBackendDelete(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	if err := s.DeleteBackend(name); err != nil {
		s.storeWriteErr(w, err, "backend delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteBackend removes the named backend from the store and reloads the cron
// scheduler. Returns *store.ErrConflict if any agent still references the
// backend (HTTP 409 / MCP user error via storeErrStatus).
//
// Exposed so non-HTTP surfaces (e.g. the MCP delete_backend tool) can run the
// same delete path as DELETE /backends/{name} without going through the router.
func (s *Server) DeleteBackend(name string) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if err := store.DeleteBackend(s.db, name); err != nil {
		return err
	}
	return s.reloadCron()
}

// ── /api/store/repos ──────────────────────────────────────────────────────────

// handleStoreRepos serves GET and POST /api/store/repos.
func (s *Server) handleStoreRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		repos, err := store.ReadRepos(s.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read repos: %v", err), http.StatusInternalServerError)
			return
		}
		out := make([]storeRepoJSON, len(repos))
		for i, r := range repos {
			out[i] = repoToStoreJSON(r)
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req storeRepoJSON
		if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		canonical, err := s.UpsertRepo(req.toConfig())
		if err != nil {
			s.storeWriteErr(w, err, "repo upsert or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, repoToStoreJSON(canonical))
	}
}

// UpsertRepo writes a single repo definition (and its bindings) into the store
// and reloads the cron scheduler. Returns the canonical (normalized) form so
// callers can surface the exact shape REST clients see in the POST /repos
// response — lowercase repo name, lowercased binding agents, trimmed cron, and
// lowercased events.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors via storeErrStatus, mirroring the
// behaviour of UpsertAgent / UpsertSkill / UpsertBackend.
//
// Exposed so non-HTTP surfaces (e.g. the MCP create_repo tool) can run the
// same upsert path as POST /repos without going through the router.
func (s *Server) UpsertRepo(r fleet.Repo) (fleet.Repo, error) {
	if strings.TrimSpace(r.Name) == "" {
		return fleet.Repo{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertRepo(s.db, r)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return fleet.Repo{}, err
	}
	fleet.NormalizeRepo(&r)
	return r, nil
}

// repoRuntimeSettingsJSON is the wire shape for PATCH /repos/{owner}/{repo}.
// Only the enabled flag is currently mutable via PATCH; name changes require
// a delete+create since the name is the primary key.
type repoRuntimeSettingsJSON struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// handleStoreRepo serves GET, PATCH, and DELETE /api/store/repos/{owner}/{repo}.
func (s *Server) handleStoreRepo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	repoName := fleet.NormalizeRepoName(vars["owner"]) + "/" + fleet.NormalizeRepoName(vars["repo"])
	switch r.Method {
	case http.MethodGet:
		repos, err := store.ReadRepos(s.db)
		if err != nil {
			http.Error(w, fmt.Sprintf("read repos: %v", err), http.StatusInternalServerError)
			return
		}
		for _, repo := range repos {
			if repo.Name == repoName {
				writeJSON(w, http.StatusOK, repoToStoreJSON(repo))
				return
			}
		}
		http.NotFound(w, r)

	case http.MethodPatch:
		var req repoRuntimeSettingsJSON
		if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
			return
		}
		if req.Enabled == nil {
			http.Error(w, "at least one field is required", http.StatusBadRequest)
			return
		}
		repo, err := s.PatchRepo(repoName, *req.Enabled)
		if err != nil {
			s.storeWriteErr(w, err, "repo patch or cron reload")
			return
		}
		writeJSON(w, http.StatusOK, repoToStoreJSON(repo))

	case http.MethodDelete:
		if err := s.DeleteRepo(repoName); err != nil {
			s.storeWriteErr(w, err, "repo delete or cron reload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// PatchRepo updates the enabled flag on an existing repo without touching its
// bindings. Returns the canonical Repo (with current bindings) so callers
// can refresh their view. *ErrNotFound when the repo does not exist.
func (s *Server) PatchRepo(repoName string, enabled bool) (fleet.Repo, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	// Load the repo (and its bindings) so we can rewrite it intact without
	// having to pipe every field through the wire struct.
	repos, err := store.ReadRepos(s.db)
	if err != nil {
		return fleet.Repo{}, err
	}
	var existing *fleet.Repo
	for i := range repos {
		if repos[i].Name == repoName {
			existing = &repos[i]
			break
		}
	}
	if existing == nil {
		return fleet.Repo{}, &store.ErrNotFound{Msg: fmt.Sprintf("repo %q not found", repoName)}
	}
	if existing.Enabled == enabled {
		return *existing, nil
	}
	// Flip the enabled flag via a direct UPDATE so we don't re-run the
	// delete+insert cycle that UpsertRepo performs on bindings.
	if _, err := s.db.Exec("UPDATE repos SET enabled=? WHERE name=?", boolToInt(enabled), repoName); err != nil {
		return fleet.Repo{}, fmt.Errorf("patch repo %s: %w", repoName, err)
	}
	if err := s.reloadCron(); err != nil {
		return fleet.Repo{}, err
	}
	existing.Enabled = enabled
	return *existing, nil
}

// boolToInt matches store.boolToInt (unexported there) for the tiny PATCH
// path above. Duplicating a 6-line helper keeps the store package closed.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DeleteRepo removes the named repo (and cascades its bindings) from the
// store and reloads the cron scheduler. Missing names are idempotent no-ops,
// matching DELETE /repos/{owner}/{repo}. Returns *store.ErrConflict if the
// deletion would leave the fleet with no enabled repos.
//
// Exposed so non-HTTP surfaces (e.g. the MCP delete_repo tool) can run the
// same delete path as DELETE /repos/{owner}/{repo} without going through the
// router.
func (s *Server) DeleteRepo(name string) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if err := store.DeleteRepo(s.db, name); err != nil {
		return err
	}
	return s.reloadCron()
}

// ── /repos/{owner}/{repo}/bindings[/{id}] — atomic binding CRUD ──────────────

// repoNameFromVars reconstructs the normalized owner/repo path parameter.
func repoNameFromVars(r *http.Request) string {
	vars := mux.Vars(r)
	return fleet.NormalizeRepoName(vars["owner"]) + "/" + fleet.NormalizeRepoName(vars["repo"])
}

// bindingIDFromVars parses the {id} path parameter. On error it writes a 400
// response and returns (0, false).
func bindingIDFromVars(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(mux.Vars(r)["id"])
	if raw == "" {
		http.Error(w, "binding id is required", http.StatusBadRequest)
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, fmt.Sprintf("invalid binding id %q", raw), http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// handleCreateBinding serves POST /repos/{owner}/{repo}/bindings. The body
// is a storeBindingJSON (ID ignored). Returns 201 with the persisted binding
// including its generated ID.
func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	var req storeBindingJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	// Ignore any ID the client sends — the store picks it.
	req.ID = 0
	b, err := s.CreateBinding(repoName, req.toConfig())
	if err != nil {
		s.storeWriteErr(w, err, "binding create or cron reload")
		return
	}
	writeJSON(w, http.StatusCreated, bindingToStoreJSON(b))
}

// CreateBinding persists a new binding on repoName and reloads cron. Exposed
// for non-HTTP callers (MCP create_binding tool).
func (s *Server) CreateBinding(repoName string, b fleet.Binding) (fleet.Binding, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	_, persisted, err := store.CreateBinding(s.db, repoName, b)
	if err != nil {
		return fleet.Binding{}, err
	}
	if err := s.reloadCron(); err != nil {
		return fleet.Binding{}, err
	}
	return persisted, nil
}

// handleGetBinding serves GET /repos/{owner}/{repo}/bindings/{id}. Returns
// 404 when the id does not exist or belongs to a different repo.
func (s *Server) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	owner, b, found, err := store.ReadBinding(s.db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("read binding: %v", err), http.StatusInternalServerError)
		return
	}
	if !found || owner != repoName {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, bindingToStoreJSON(b))
}

// handleUpdateBinding serves PATCH /repos/{owner}/{repo}/bindings/{id}. The
// body is a storeBindingJSON; all fields are replaced. Returns 404 if the id
// does not belong to the repo in the path.
func (s *Server) handleUpdateBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	var req storeBindingJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	b, err := s.UpdateBinding(repoName, id, req.toConfig())
	if err != nil {
		s.storeWriteErr(w, err, "binding update or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, bindingToStoreJSON(b))
}

// UpdateBinding verifies the id belongs to repoName, replaces the row, and
// reloads cron. Exposed for non-HTTP callers (MCP update_binding tool).
func (s *Server) UpdateBinding(repoName string, id int64, b fleet.Binding) (fleet.Binding, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	existingRepo, _, found, err := store.ReadBinding(s.db, id)
	if err != nil {
		return fleet.Binding{}, err
	}
	if !found || existingRepo != repoName {
		return fleet.Binding{}, &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	updated, err := store.UpdateBinding(s.db, id, b)
	if err != nil {
		return fleet.Binding{}, err
	}
	if err := s.reloadCron(); err != nil {
		return fleet.Binding{}, err
	}
	return updated, nil
}

// handleDeleteBinding serves DELETE /repos/{owner}/{repo}/bindings/{id}.
// Returns 404 if the id does not belong to the repo in the path.
func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	if err := s.DeleteBinding(repoName, id); err != nil {
		s.storeWriteErr(w, err, "binding delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReadBinding fetches one binding by ID, verifying it belongs to repoName.
// Exposed for non-HTTP callers (MCP get_binding tool).
func (s *Server) ReadBinding(repoName string, id int64) (fleet.Binding, error) {
	existingRepo, b, found, err := store.ReadBinding(s.db, id)
	if err != nil {
		return fleet.Binding{}, err
	}
	if !found || existingRepo != repoName {
		return fleet.Binding{}, &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	return b, nil
}

// DeleteBinding verifies the id belongs to repoName, deletes it, and reloads
// cron. Exposed for non-HTTP callers (MCP delete_binding tool).
func (s *Server) DeleteBinding(repoName string, id int64) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	existingRepo, _, found, err := store.ReadBinding(s.db, id)
	if err != nil {
		return err
	}
	if !found || existingRepo != repoName {
		return &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	if err := store.DeleteBinding(s.db, id); err != nil {
		return err
	}
	return s.reloadCron()
}

// ── /api/store/export and /api/store/import ───────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// four CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded — it is not managed by the write API.
type exportYAML struct {
	Skills map[string]fleet.Skill `yaml:"skills,omitempty"`
	Agents []fleet.Agent          `yaml:"agents,omitempty"`
	Repos  []fleet.Repo           `yaml:"repos,omitempty"`
	Daemon *exportDaemonYAML          `yaml:"daemon,omitempty"`
}

type exportDaemonYAML struct {
	AIBackends map[string]fleet.Backend `yaml:"ai_backends,omitempty"`
}

// handleStoreExport serves GET /api/store/export — returns a config.yaml
// fragment covering the four CRUD-mutable sections (skills, agents, repos,
// daemon.ai_backends). The API key is required because backends may contain
// secret env values.
func (s *Server) handleStoreExport(w http.ResponseWriter, _ *http.Request) {
	b, err := s.ExportYAML()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="config-export.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// ExportYAML returns the CRUD-mutable sections of the store as a YAML fragment
// matching the GET /export response body. Exposed so non-HTTP surfaces (e.g.
// the MCP export_config tool) can serve the same payload the REST endpoint
// returns without going through the router.
func (s *Server) ExportYAML() ([]byte, error) {
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	out := exportYAML{
		Skills: skills,
		Agents: agents,
		Repos:  repos,
	}
	if len(backends) > 0 {
		out.Daemon = &exportDaemonYAML{AIBackends: backends}
	}
	b, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	return b, nil
}

// handleStoreImport serves POST /api/store/import — accepts a YAML body in
// the same format as handleStoreExport and upserts all entities into the DB.
// On success it returns 200 with a JSON summary of imported counts.
func (s *Server) handleStoreImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.loadCfg().Daemon.HTTP.MaxBodyBytes*10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return
	}
	counts, err := s.ImportYAML(body, r.URL.Query().Get("mode"))
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

// ImportYAML parses a YAML payload in handleStoreExport's format and writes it
// to the store. mode controls upsert semantics: empty or "merge" preserves
// existing records, "replace" prunes anything not present in the payload.
//
// On success it returns the per-section counts that handleStoreImport ships in
// its JSON response. Validation failures (bad mode, malformed YAML, store-level
// invariants) are returned as *store.ErrValidation so callers can map them to
// HTTP 400 / MCP user errors via storeErrStatus.
//
// Exposed so non-HTTP surfaces (e.g. the MCP import_config tool) can run the
// same import path as POST /import without going through the router.
func (s *Server) ImportYAML(body []byte, mode string) (map[string]int, error) {
	if mode != "" && mode != "merge" && mode != "replace" {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("invalid mode %q: must be empty, \"merge\", or \"replace\"", mode)}
	}
	var payload exportYAML
	if err := yaml.Unmarshal(body, &payload); err != nil {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("parse yaml: %v", err)}
	}

	backends := map[string]fleet.Backend{}
	if payload.Daemon != nil {
		backends = payload.Daemon.AIBackends
	}

	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	var importErr error
	if mode == "replace" {
		importErr = store.ReplaceAll(s.db, payload.Agents, payload.Repos, payload.Skills, backends)
	} else {
		importErr = store.ImportAll(s.db, payload.Agents, payload.Repos, payload.Skills, backends)
	}
	if importErr != nil {
		return nil, fmt.Errorf("import: %w", importErr)
	}
	if err := s.reloadCron(); err != nil {
		s.logger.Error().Err(err).Msg("store import: cron reload failed")
		return nil, fmt.Errorf("cron reload: %w", err)
	}

	return map[string]int{
		"agents":   len(payload.Agents),
		"skills":   len(payload.Skills),
		"repos":    len(payload.Repos),
		"backends": len(backends),
	}, nil
}
