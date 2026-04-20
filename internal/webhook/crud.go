package webhook

import (
	"encoding/json"
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
	return storeBackendJSON{
		Name:             name,
		Command:          b.Command,
		Args:             nilSafeStrings(b.Args),
		Env:              b.Env,
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

func storeNotConfigured(w http.ResponseWriter) {
	http.Error(w, "store not configured (start daemon with --db)", http.StatusNotImplemented)
}

// reloadCron re-reads repos and agents from the DB as a consistent snapshot
// and calls Reload on the attached CronReloader (if any). Both datasets are
// read within a single transaction so a concurrent /api/store write cannot
// produce a mixed snapshot that combines repos from one commit point with
// agents from another. An error is returned so callers can surface scheduler
// failures as HTTP 500 responses — the DB write already succeeded but the
// in-memory cron state would be stale or broken if we ignored the error.
func (s *Server) reloadCron() error {
	if s.cronReloader == nil {
		return nil
	}
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return fmt.Errorf("read config snapshot for cron reload: %w", err)
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
		if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := store.UpsertAgent(s.db, req.toConfig()); err != nil {
			http.Error(w, fmt.Sprintf("upsert agent: %v", err), http.StatusInternalServerError)
			return
		}
		if err := s.reloadCron(); err != nil {
			s.logger.Error().Err(err).Msg("store crud: cron reload failed after agent upsert")
			http.Error(w, fmt.Sprintf("cron reload: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, req)
	}
}

// handleStoreAgent serves GET and DELETE /api/store/agents/{name}.
func (s *Server) handleStoreAgent(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
	name := mux.Vars(r)["name"]
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
		if err := store.DeleteAgent(s.db, name); err != nil {
			http.Error(w, fmt.Sprintf("delete agent: %v", err), http.StatusInternalServerError)
			return
		}
		if err := s.reloadCron(); err != nil {
			s.logger.Error().Err(err).Msg("store crud: cron reload failed after agent delete")
			http.Error(w, fmt.Sprintf("cron reload: %v", err), http.StatusInternalServerError)
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
		if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := store.UpsertSkill(s.db, req.Name, config.SkillDef{Prompt: req.Prompt}); err != nil {
			http.Error(w, fmt.Sprintf("upsert skill: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, req)
	}
}

// handleStoreSkill serves GET and DELETE /api/store/skills/{name}.
func (s *Server) handleStoreSkill(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
	name := mux.Vars(r)["name"]
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
		if err := store.DeleteSkill(s.db, name); err != nil {
			http.Error(w, fmt.Sprintf("delete skill: %v", err), http.StatusInternalServerError)
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
		if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Env == nil {
			req.Env = map[string]string{}
		}
		if err := store.UpsertBackend(s.db, req.Name, req.toConfig()); err != nil {
			http.Error(w, fmt.Sprintf("upsert backend: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, req)
	}
}

// handleStoreBackend serves GET and DELETE /api/store/backends/{name}.
func (s *Server) handleStoreBackend(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
	name := mux.Vars(r)["name"]
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
		if err := store.DeleteBackend(s.db, name); err != nil {
			http.Error(w, fmt.Sprintf("delete backend: %v", err), http.StatusInternalServerError)
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
		if err := json.NewDecoder(io.LimitReader(r.Body, s.cfg.Daemon.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := store.UpsertRepo(s.db, req.toConfig()); err != nil {
			http.Error(w, fmt.Sprintf("upsert repo: %v", err), http.StatusInternalServerError)
			return
		}
		if err := s.reloadCron(); err != nil {
			s.logger.Error().Err(err).Msg("store crud: cron reload failed after repo upsert")
			http.Error(w, fmt.Sprintf("cron reload: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, req)
	}
}

// handleStoreRepo serves GET and DELETE /api/store/repos/{owner}/{repo}.
func (s *Server) handleStoreRepo(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		storeNotConfigured(w)
		return
	}
	vars := mux.Vars(r)
	repoName := vars["owner"] + "/" + vars["repo"]
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
		if err := store.DeleteRepo(s.db, repoName); err != nil {
			http.Error(w, fmt.Sprintf("delete repo: %v", err), http.StatusInternalServerError)
			return
		}
		if err := s.reloadCron(); err != nil {
			s.logger.Error().Err(err).Msg("store crud: cron reload failed after repo delete")
			http.Error(w, fmt.Sprintf("cron reload: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
