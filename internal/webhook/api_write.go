package webhook

// api_write.go contains the CRUD mutation handlers for the write API.
// These endpoints are only registered when the server is started with a SQLite
// database (--db flag). After each successful write the server reloads its
// in-memory config from the database so that subsequent GET requests reflect
// the change without a restart.
//
// All write endpoints require the same bearer-token auth as POST /agents/run.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// maxWriteBodyBytes caps request bodies for write endpoints.
const maxWriteBodyBytes = 1 << 20 // 1 MiB

// ── /api/agents ─────────────────────────────────────────────────────────────

// putAgentRequest is the wire shape for PUT /api/agents.
type putAgentRequest struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Skills        []string `json:"skills"`
	Prompt        string   `json:"prompt"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	CanDispatch   []string `json:"can_dispatch"`
	Description   string   `json:"description"`
}

// handlePutAgent handles PUT /api/agents — creates or updates an agent.
func (s *Server) handlePutAgent(w http.ResponseWriter, r *http.Request) {
	var req putAgentRequest
	if err := decodeBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	a := config.AgentDef{
		Name:          req.Name,
		Backend:       req.Backend,
		Skills:        req.Skills,
		Prompt:        req.Prompt,
		AllowPRs:      req.AllowPRs,
		AllowDispatch: req.AllowDispatch,
		CanDispatch:   req.CanDispatch,
		Description:   req.Description,
	}
	if err := store.PutAgent(s.db, a); err != nil {
		s.logger.Error().Err(err).Str("agent", req.Name).Msg("put agent")
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after put agent")
		// Non-fatal: the DB write succeeded; the in-memory view may be stale
		// until next restart, but the data is persisted.
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAgent handles DELETE /api/agents/{name}.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := store.DeleteAgent(s.db, name); err != nil {
		s.logger.Error().Err(err).Str("agent", name).Msg("delete agent")
		http.Error(w, "failed to delete agent", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after delete agent")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/skills ─────────────────────────────────────────────────────────────

type putSkillRequest struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// handlePutSkill handles PUT /api/skills — creates or updates a skill.
func (s *Server) handlePutSkill(w http.ResponseWriter, r *http.Request) {
	var req putSkillRequest
	if err := decodeBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := store.PutSkill(s.db, req.Name, config.SkillDef{Prompt: req.Prompt}); err != nil {
		s.logger.Error().Err(err).Str("skill", req.Name).Msg("put skill")
		http.Error(w, "failed to save skill", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after put skill")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSkill handles DELETE /api/skills/{name}.
func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := store.DeleteSkill(s.db, name); err != nil {
		s.logger.Error().Err(err).Str("skill", name).Msg("delete skill")
		http.Error(w, "failed to delete skill", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after delete skill")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/backends ────────────────────────────────────────────────────────────

type putBackendRequest struct {
	Name             string            `json:"name"`
	Command          string            `json:"command"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MaxPromptChars   int               `json:"max_prompt_chars"`
	RedactionSaltEnv string            `json:"redaction_salt_env"`
}

// handlePutBackend handles PUT /api/backends — creates or updates a backend.
func (s *Server) handlePutBackend(w http.ResponseWriter, r *http.Request) {
	var req putBackendRequest
	if err := decodeBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	b := config.AIBackendConfig{
		Command:          req.Command,
		Args:             req.Args,
		Env:              req.Env,
		TimeoutSeconds:   req.TimeoutSeconds,
		MaxPromptChars:   req.MaxPromptChars,
		RedactionSaltEnv: req.RedactionSaltEnv,
	}
	if err := store.PutBackend(s.db, req.Name, b); err != nil {
		s.logger.Error().Err(err).Str("backend", req.Name).Msg("put backend")
		http.Error(w, "failed to save backend", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after put backend")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteBackend handles DELETE /api/backends/{name}.
func (s *Server) handleDeleteBackend(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := store.DeleteBackend(s.db, name); err != nil {
		s.logger.Error().Err(err).Str("backend", name).Msg("delete backend")
		http.Error(w, "failed to delete backend", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after delete backend")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/repos ───────────────────────────────────────────────────────────────

type putRepoRequest struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// handlePutRepo handles PUT /api/repos — creates or updates a repo.
// Bindings are managed separately via the /api/repos/{name}/bindings endpoints.
func (s *Server) handlePutRepo(w http.ResponseWriter, r *http.Request) {
	var req putRepoRequest
	if err := decodeBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	repo := config.RepoDef{Name: req.Name, Enabled: req.Enabled}
	if err := store.PutRepo(s.db, repo); err != nil {
		s.logger.Error().Err(err).Str("repo", req.Name).Msg("put repo")
		http.Error(w, "failed to save repo", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after put repo")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteRepo handles DELETE /api/repos/{name}.
// This also deletes all bindings for that repo.
func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if err := store.DeleteRepo(s.db, name); err != nil {
		s.logger.Error().Err(err).Str("repo", name).Msg("delete repo")
		http.Error(w, "failed to delete repo", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after delete repo")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/repos/{name}/bindings ───────────────────────────────────────────────

type putBindingRequest struct {
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels"`
	Events  []string `json:"events"`
	Cron    string   `json:"cron"`
	Enabled *bool    `json:"enabled"`
}

type putBindingResponse struct {
	ID int64 `json:"id"`
}

// handlePutBinding handles POST /api/repos/{name}/bindings — adds a binding.
func (s *Server) handlePutBinding(w http.ResponseWriter, r *http.Request) {
	repo := mux.Vars(r)["name"]
	var req putBindingRequest
	if err := decodeBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Agent == "" {
		http.Error(w, "agent is required", http.StatusBadRequest)
		return
	}
	b := config.Binding{
		Agent:   req.Agent,
		Labels:  req.Labels,
		Events:  req.Events,
		Cron:    req.Cron,
		Enabled: req.Enabled,
	}
	id, err := store.PutBinding(s.db, repo, b)
	if err != nil {
		s.logger.Error().Err(err).Str("repo", repo).Str("agent", req.Agent).Msg("put binding")
		http.Error(w, "failed to save binding", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after put binding")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(putBindingResponse{ID: id})
}

// handleDeleteBinding handles DELETE /api/repos/{name}/bindings/{id}.
func (s *Server) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	idStr := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid binding id", http.StatusBadRequest)
		return
	}
	if err := store.DeleteBinding(s.db, id); err != nil {
		s.logger.Error().Err(err).Int64("id", id).Msg("delete binding")
		http.Error(w, "failed to delete binding", http.StatusInternalServerError)
		return
	}
	if err := s.reloadConfig(); err != nil {
		s.logger.Error().Err(err).Msg("reload config after delete binding")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// decodeBody reads and JSON-decodes a request body, enforcing a 1 MiB size cap.
func decodeBody(r *http.Request, dst any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxWriteBodyBytes)).Decode(dst); err != nil {
		return err
	}
	return nil
}
