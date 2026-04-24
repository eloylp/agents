package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/backends"
	"github.com/eloylp/agents/internal/config"
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
}

func agentToStoreJSON(a config.AgentDef) storeAgentJSON {
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
	}
}

func (j storeAgentJSON) toConfig() config.AgentDef {
	return config.AgentDef{
		Name:          j.Name,
		Backend:       j.Backend,
		Model:         j.Model,
		Skills:        nilSafeStrings(j.Skills),
		Prompt:        j.Prompt,
		AllowPRs:      j.AllowPRs,
		AllowDispatch: j.AllowDispatch,
		CanDispatch:   nilSafeStrings(j.CanDispatch),
		Description:   j.Description,
	}
}

type storeSkillJSON struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
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

type backendRuntimeSettingsJSON struct {
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
	MaxPromptChars *int `json:"max_prompt_chars,omitempty"`
}

func backendToStoreJSON(name string, b config.AIBackendConfig) storeBackendJSON {
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

func (j storeBackendJSON) toConfig() config.AIBackendConfig {
	return config.AIBackendConfig{
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
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Events  []string `json:"events,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

type storeRepoJSON struct {
	Name     string             `json:"name"`
	Enabled  bool               `json:"enabled"`
	Bindings []storeBindingJSON `json:"bindings"`
}

func repoToStoreJSON(r config.RepoDef) storeRepoJSON {
	bindings := make([]storeBindingJSON, len(r.Use))
	for i, b := range r.Use {
		// Always emit the effective enabled state (default true when nil)
		// so consumers see a stable bool shape.
		enabled := b.IsEnabled()
		bindings[i] = storeBindingJSON{
			Agent:   b.Agent,
			Labels:  b.Labels,
			Events:  b.Events,
			Cron:    b.Cron,
			Enabled: &enabled,
		}
	}
	return storeRepoJSON{Name: r.Name, Enabled: r.Enabled, Bindings: bindings}
}

func (j storeRepoJSON) toConfig() config.RepoDef {
	use := make([]config.Binding, len(j.Bindings))
	for i, b := range j.Bindings {
		use[i] = config.Binding{
			Agent:   b.Agent,
			Labels:  b.Labels,
			Events:  b.Events,
			Cron:    b.Cron,
			Enabled: b.Enabled,
		}
	}
	return config.RepoDef{Name: j.Name, Enabled: j.Enabled, Use: use}
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
// violations, referenced-by failures) → 409, anything else → 500.
func storeErrStatus(err error) int {
	var valErr *store.ErrValidation
	if errors.As(err, &valErr) {
		return http.StatusBadRequest
	}
	var conflictErr *store.ErrConflict
	if errors.As(err, &conflictErr) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
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
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: agent upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("agent upsert or cron reload: %v", err), status)
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
func (s *Server) UpsertAgent(a config.AgentDef) (config.AgentDef, error) {
	if strings.TrimSpace(a.Name) == "" {
		return config.AgentDef{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertAgent(s.db, a)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return config.AgentDef{}, err
	}
	config.NormalizeAgentDef(&a)
	return a, nil
}

// handleStoreAgent serves GET and DELETE /api/store/agents/{name}.
func (s *Server) handleStoreAgent(w http.ResponseWriter, r *http.Request) {
	name := config.NormalizeAgentName(mux.Vars(r)["name"])
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

	case http.MethodDelete:
		cascade := r.URL.Query().Get("cascade") == "true"
		if err := s.DeleteAgent(name, cascade); err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: agent delete or cron reload failed")
			http.Error(w, fmt.Sprintf("agent delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
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
		name, sk, err := s.UpsertSkill(req.Name, config.SkillDef{Prompt: req.Prompt})
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: skill upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("skill upsert or cron reload: %v", err), status)
			return
		}
		writeJSON(w, http.StatusOK, storeSkillJSON{Name: name, Prompt: sk.Prompt})
	}
}

// UpsertSkill writes a single skill into the store and reloads the cron
// scheduler. Returns the canonical (normalized) name and SkillDef that were
// persisted so callers can surface the same shape REST clients see in the
// POST response — lowercase/trimmed name, trimmed prompt.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors via storeErrStatus, mirroring the
// behaviour of UpsertAgent and POST /skills.
//
// Exposed so non-HTTP surfaces (e.g. the MCP create_skill tool) can run the
// same upsert path as POST /skills without going through the router.
func (s *Server) UpsertSkill(name string, sk config.SkillDef) (string, config.SkillDef, error) {
	if strings.TrimSpace(name) == "" {
		return "", config.SkillDef{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertSkill(s.db, name, sk)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return "", config.SkillDef{}, err
	}
	config.NormalizeSkillDef(&sk)
	return config.NormalizeSkillName(name), sk, nil
}

// handleStoreSkill serves GET and DELETE /api/store/skills/{name}.
func (s *Server) handleStoreSkill(w http.ResponseWriter, r *http.Request) {
	name := config.NormalizeSkillName(mux.Vars(r)["name"])
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

	case http.MethodDelete:
		if err := s.DeleteSkill(name); err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: skill delete or cron reload failed")
			http.Error(w, fmt.Sprintf("skill delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
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
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: backend upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("backend upsert or cron reload: %v", err), status)
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
func (s *Server) UpsertBackend(name string, b config.AIBackendConfig) (string, config.AIBackendConfig, error) {
	if strings.TrimSpace(name) == "" {
		return "", config.AIBackendConfig{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertBackend(s.db, name, b)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return "", config.AIBackendConfig{}, err
	}
	config.NormalizeBackendConfig(&b)
	config.ApplyBackendDefaults(&b)
	return config.NormalizeBackendName(name), b, nil
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
	name := config.NormalizeBackendName(req.Name)
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

	diagMap := map[string]config.AIBackendConfig{
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
	name := config.NormalizeBackendName(mux.Vars(r)["name"])
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

// handleStoreBackendPatch serves PATCH /api/store/backends/{name}.
func (s *Server) handleStoreBackendPatch(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)

	var req backendRuntimeSettingsJSON
	if !decodeBody(w, r, s.loadCfg().Daemon.HTTP.MaxBodyBytes, &req) {
		return
	}
	if req.TimeoutSeconds == nil && req.MaxPromptChars == nil {
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

	s.storeMu.Lock()
	backendsByName, err := store.ReadBackends(s.db)
	if err != nil {
		s.storeMu.Unlock()
		http.Error(w, fmt.Sprintf("read backends: %v", err), http.StatusInternalServerError)
		return
	}
	b, ok := backendsByName[name]
	if !ok {
		s.storeMu.Unlock()
		http.NotFound(w, r)
		return
	}
	if req.TimeoutSeconds != nil {
		b.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.MaxPromptChars != nil {
		b.MaxPromptChars = *req.MaxPromptChars
	}

	err = store.UpsertBackend(s.db, name, b)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		status := storeErrStatus(err)
		s.logger.Error().Err(err).Msg("store crud: backend runtime settings update or cron reload failed")
		http.Error(w, fmt.Sprintf("backend runtime settings update or cron reload: %v", err), status)
		return
	}
	writeJSON(w, http.StatusOK, backendToStoreJSON(name, b))
}

// handleStoreBackendDelete serves DELETE /api/store/backends/{name}.
func (s *Server) handleStoreBackendDelete(w http.ResponseWriter, r *http.Request) {
	name := backendPathName(r)
	if err := s.DeleteBackend(name); err != nil {
		status := storeErrStatus(err)
		s.logger.Error().Err(err).Msg("store crud: backend delete or cron reload failed")
		http.Error(w, fmt.Sprintf("backend delete or cron reload: %v", err), status)
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
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: repo upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("repo upsert or cron reload: %v", err), status)
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
func (s *Server) UpsertRepo(r config.RepoDef) (config.RepoDef, error) {
	if strings.TrimSpace(r.Name) == "" {
		return config.RepoDef{}, &store.ErrValidation{Msg: "name is required"}
	}
	s.storeMu.Lock()
	err := store.UpsertRepo(s.db, r)
	if err == nil {
		err = s.reloadCron()
	}
	s.storeMu.Unlock()
	if err != nil {
		return config.RepoDef{}, err
	}
	config.NormalizeRepoDef(&r)
	return r, nil
}

// handleStoreRepo serves GET and DELETE /api/store/repos/{owner}/{repo}.
func (s *Server) handleStoreRepo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	repoName := config.NormalizeRepoName(vars["owner"]) + "/" + config.NormalizeRepoName(vars["repo"])
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

	case http.MethodDelete:
		if err := s.DeleteRepo(repoName); err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: repo delete or cron reload failed")
			http.Error(w, fmt.Sprintf("repo delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
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

// ── /api/store/export and /api/store/import ───────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// four CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded — it is not managed by the write API.
type exportYAML struct {
	Skills map[string]config.SkillDef `yaml:"skills,omitempty"`
	Agents []config.AgentDef          `yaml:"agents,omitempty"`
	Repos  []config.RepoDef           `yaml:"repos,omitempty"`
	Daemon *exportDaemonYAML          `yaml:"daemon,omitempty"`
}

type exportDaemonYAML struct {
	AIBackends map[string]config.AIBackendConfig `yaml:"ai_backends,omitempty"`
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

	backends := map[string]config.AIBackendConfig{}
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
