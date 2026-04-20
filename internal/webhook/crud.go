package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// ── JSON wire types ───────────────────────────────────────────────────────────

type storeAgentJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
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
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MaxPromptChars   int               `json:"max_prompt_chars"`
	RedactionSaltEnv string            `json:"redaction_salt_env"`
}

func backendToStoreJSON(name string, b config.AIBackendConfig) storeBackendJSON {
	// Redact env values: backend env entries are expected to hold secrets
	// (API keys, base URLs, etc.). Preserve the key names so operators can
	// identify which environment variables are configured, but never expose
	// the resolved values — consistent with how /api/config handles backends.
	redactedEnv := make(map[string]string, len(b.Env))
	for k := range b.Env {
		redactedEnv[k] = "[redacted]"
	}
	return storeBackendJSON{
		Name:             name,
		Command:          b.Command,
		Args:             nilSafeStrings(b.Args),
		Env:              redactedEnv,
		TimeoutSeconds:   b.TimeoutSeconds,
		MaxPromptChars:   b.MaxPromptChars,
		RedactionSaltEnv: b.RedactionSaltEnv,
	}
}

func (j storeBackendJSON) toConfig() config.AIBackendConfig {
	return config.AIBackendConfig{
		Command:          j.Command,
		Args:             nilSafeStrings(j.Args),
		Env:              j.Env,
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
		bindings[i] = storeBindingJSON{
			Agent:   b.Agent,
			Labels:  b.Labels,
			Events:  b.Events,
			Cron:    b.Cron,
			Enabled: b.Enabled,
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

func storeNotConfigured(w http.ResponseWriter) {
	http.Error(w, "store not configured (start daemon with --db)", http.StatusNotImplemented)
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

	// Update the server's in-memory routing config so that webhook event
	// handlers (/webhooks/github, /agents/run) and read APIs (/api/agents,
	// /api/config) reflect the post-write state immediately without a restart.
	// Copy-on-write: build a new config value from the current snapshot,
	// replacing only the four CRUD-mutable fields. Daemon-level config (HTTP,
	// proxy, log) is never changed by CRUD writes and is preserved unchanged.
	s.cfgMu.Lock()
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends
	s.cfg = &newCfg
	s.cfgMu.Unlock()

	if s.cronReloader == nil {
		return nil
	}
	return s.cronReloader.Reload(repos, agents, skills, backends)
}

// ── /api/store/agents ────────────────────────────────────────────────────────

// handleStoreAgents serves GET and POST /api/store/agents.
func (s *Server) handleStoreAgents(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		s.storeMu.Lock()
		err := store.UpsertAgent(s.db, req.toConfig())
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: agent upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("agent upsert or cron reload: %v", err), status)
			return
		}
		// Return the canonical persisted form so clients see normalized values
		// (lowercase name, trimmed prompt, etc.) rather than the raw request.
		a := req.toConfig()
		config.NormalizeAgentDef(&a)
		writeJSON(w, http.StatusOK, agentToStoreJSON(a))
	}
}

// handleStoreAgent serves GET and DELETE /api/store/agents/{name}.
func (s *Server) handleStoreAgent(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		s.storeMu.Lock()
		err := store.DeleteAgent(s.db, name)
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: agent delete or cron reload failed")
			http.Error(w, fmt.Sprintf("agent delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── /api/store/skills ─────────────────────────────────────────────────────────

// handleStoreSkills serves GET and POST /api/store/skills.
func (s *Server) handleStoreSkills(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		s.storeMu.Lock()
		err := store.UpsertSkill(s.db, req.Name, config.SkillDef{Prompt: req.Prompt})
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: skill upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("skill upsert or cron reload: %v", err), status)
			return
		}
		// Return the canonical persisted form so clients see normalized values.
		sk := config.SkillDef{Prompt: req.Prompt}
		config.NormalizeSkillDef(&sk)
		writeJSON(w, http.StatusOK, storeSkillJSON{Name: config.NormalizeSkillName(req.Name), Prompt: sk.Prompt})
	}
}

// handleStoreSkill serves GET and DELETE /api/store/skills/{name}.
func (s *Server) handleStoreSkill(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		s.storeMu.Lock()
		err := store.DeleteSkill(s.db, name)
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: skill delete or cron reload failed")
			http.Error(w, fmt.Sprintf("skill delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── /api/store/backends ───────────────────────────────────────────────────────

// handleStoreBackends serves GET and POST /api/store/backends.
func (s *Server) handleStoreBackends(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Env == nil {
			req.Env = map[string]string{}
		}
		s.storeMu.Lock()
		err := store.UpsertBackend(s.db, req.Name, req.toConfig())
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: backend upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("backend upsert or cron reload: %v", err), status)
			return
		}
		// Return the canonical persisted form: normalized name, defaults applied,
		// and env values redacted — consistent with what GET returns.
		b := req.toConfig()
		config.NormalizeBackendConfig(&b)
		config.ApplyBackendDefaults(&b)
		writeJSON(w, http.StatusOK, backendToStoreJSON(config.NormalizeBackendName(req.Name), b))
	}
}

// handleStoreBackend serves GET and DELETE /api/store/backends/{name}.
func (s *Server) handleStoreBackend(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
	name := config.NormalizeBackendName(mux.Vars(r)["name"])
	switch r.Method {
	case http.MethodGet:
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

	case http.MethodDelete:
		s.storeMu.Lock()
		err := store.DeleteBackend(s.db, name)
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: backend delete or cron reload failed")
			http.Error(w, fmt.Sprintf("backend delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── /api/store/repos ──────────────────────────────────────────────────────────

// handleStoreRepos serves GET and POST /api/store/repos.
func (s *Server) handleStoreRepos(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		s.storeMu.Lock()
		err := store.UpsertRepo(s.db, req.toConfig())
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: repo upsert or cron reload failed")
			http.Error(w, fmt.Sprintf("repo upsert or cron reload: %v", err), status)
			return
		}
		// Return the canonical persisted form so clients see normalized values
		// (lowercase repo name, trimmed binding fields, etc.).
		r := req.toConfig()
		config.NormalizeRepoDef(&r)
		writeJSON(w, http.StatusOK, repoToStoreJSON(r))
	}
}

// handleStoreRepo serves GET and DELETE /api/store/repos/{owner}/{repo}.
func (s *Server) handleStoreRepo(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
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
		s.storeMu.Lock()
		err := store.DeleteRepo(s.db, repoName)
		if err == nil {
			err = s.reloadCron()
		}
		s.storeMu.Unlock()
		if err != nil {
			status := storeErrStatus(err)
			s.logger.Error().Err(err).Msg("store crud: repo delete or cron reload failed")
			http.Error(w, fmt.Sprintf("repo delete or cron reload: %v", err), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
