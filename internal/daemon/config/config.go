// Package config implements the /config, /export, and /import HTTP surface
// plus the methods the MCP get_config / export_config / import_config tools
// call directly. Both surfaces share one canonical wire shape (the
// apiConfigJSON tree for /config; the exportYAML fragment for /export +
// /import) so REST and MCP clients see identical payloads.
//
// The handler reads CRUD-mutable fleet entities from SQLite on every request.
// Daemon runtime config is process-owned and is not exposed by /config,
// /export, or /import.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// Handler implements the /config, /export, and /import HTTP surface plus the
// methods exposed for the MCP config tools.
type Handler struct {
	store     *store.Store
	daemonCfg config.DaemonConfig
	logger    zerolog.Logger
}

// New constructs a Handler. store is the data-access facade read on every
// request to assemble the /config response from the latest committed entities.
// daemonCfg supplies process-owned request limits for import and budget writes.
func New(st *store.Store, daemonCfg config.DaemonConfig, logger zerolog.Logger) *Handler {
	return &Handler{
		store:     st,
		daemonCfg: daemonCfg,
		logger:    logger.With().Str("component", "server_config").Logger(),
	}
}

// RegisterRoutes mounts the config endpoints on r.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/config", withTimeout(http.HandlerFunc(h.HandleConfig))).Methods(http.MethodGet)
	r.Handle("/export", withTimeout(http.HandlerFunc(h.HandleExport))).Methods(http.MethodGet)
	r.Handle("/import", withTimeout(http.HandlerFunc(h.HandleImport))).Methods(http.MethodPost)
	r.Handle("/token_budgets", withTimeout(http.HandlerFunc(h.listTokenBudgets))).Methods(http.MethodGet)
	r.Handle("/token_budgets", withTimeout(http.HandlerFunc(h.createTokenBudget))).Methods(http.MethodPost)
	r.Handle("/token_budgets/alerts", withTimeout(http.HandlerFunc(h.handleTokenBudgetAlerts))).Methods(http.MethodGet)
	r.Handle("/token_budgets/{id:[0-9]+}", withTimeout(http.HandlerFunc(h.getTokenBudgetByID))).Methods(http.MethodGet)
	r.Handle("/token_budgets/{id:[0-9]+}", withTimeout(http.HandlerFunc(h.updateTokenBudgetByID))).Methods(http.MethodPatch)
	r.Handle("/token_budgets/{id:[0-9]+}", withTimeout(http.HandlerFunc(h.deleteTokenBudgetByID))).Methods(http.MethodDelete)
	r.Handle("/token_leaderboard", withTimeout(http.HandlerFunc(h.handleTokenLeaderboard))).Methods(http.MethodGet)
}

// ── /config ─────────────────────────────────────────────────────────────────

// apiConfigJSON is the fleet-only wire shape for /config.
type apiConfigJSON struct {
	Backends map[string]apiAIBackendConfigJSON `json:"backends,omitempty"`
	Skills   map[string]apiSkillJSON           `json:"skills,omitempty"`
	Agents   []apiAgentConfigJSON              `json:"agents,omitempty"`
	Repos    []apiRepoConfigJSON               `json:"repos,omitempty"`
}

// apiBindingConfigJSON is the wire shape for a repo binding in /config.
// Enabled is always an explicit bool: a nil *bool in config (meaning "default
// enabled") is normalized to true so clients see the effective value.
type apiBindingConfigJSON struct {
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Events  []string `json:"events,omitempty"`
	Enabled bool     `json:"enabled"`
}

// apiRepoConfigJSON is the wire shape for one repo in /config.
type apiRepoConfigJSON struct {
	Name     string                 `json:"name"`
	Enabled  bool                   `json:"enabled"`
	Bindings []apiBindingConfigJSON `json:"bindings,omitempty"`
}

type apiAIBackendConfigJSON struct {
	Command        string   `json:"command"`
	Version        string   `json:"version,omitempty"`
	Models         []string `json:"models,omitempty"`
	Healthy        bool     `json:"healthy"`
	HealthDetail   string   `json:"health_detail,omitempty"`
	LocalModelURL  string   `json:"local_model_url,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxPromptChars int      `json:"max_prompt_chars"`
}

type apiSkillJSON struct {
	Prompt string `json:"prompt,omitempty"`
}

type apiAgentConfigJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend,omitempty"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	Description   string   `json:"description,omitempty"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	AllowMemory   bool     `json:"allow_memory"`
	CanDispatch   []string `json:"can_dispatch,omitempty"`
}

// HandleConfig serves GET /config, the current fleet config snapshot.
func (h *Handler) HandleConfig(w http.ResponseWriter, _ *http.Request) {
	body, err := h.ConfigJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ConfigJSON returns the current fleet config snapshot as JSON bytes. Exposed
// so surfaces beyond HTTP (e.g. the MCP get_config tool) can reuse the exact
// same wire shape without going through the router.
func (h *Handler) ConfigJSON() ([]byte, error) {
	storedAgents, storedRepos, storedSkills, storedBackends, err := h.store.ReadSnapshot()
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	backends := make(map[string]apiAIBackendConfigJSON, len(storedBackends))
	for name, b := range storedBackends {
		backends[name] = apiAIBackendConfigJSON{
			Command:        b.Command,
			Version:        b.Version,
			Models:         b.Models,
			Healthy:        b.Healthy,
			HealthDetail:   b.HealthDetail,
			LocalModelURL:  b.LocalModelURL,
			TimeoutSeconds: b.TimeoutSeconds,
			MaxPromptChars: b.MaxPromptChars,
		}
	}

	skills := make(map[string]apiSkillJSON, len(storedSkills))
	for name, skill := range storedSkills {
		skills[name] = apiSkillJSON{Prompt: skill.Prompt}
	}

	agents := make([]apiAgentConfigJSON, 0, len(storedAgents))
	for _, a := range storedAgents {
		agents = append(agents, apiAgentConfigJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Model:         a.Model,
			Skills:        a.Skills,
			Prompt:        a.Prompt,
			Description:   a.Description,
			AllowPRs:      a.AllowPRs,
			AllowDispatch: a.AllowDispatch,
			AllowMemory:   a.IsAllowMemory(),
			CanDispatch:   a.CanDispatch,
		})
	}

	repos := make([]apiRepoConfigJSON, 0, len(storedRepos))
	for _, r := range storedRepos {
		bindings := make([]apiBindingConfigJSON, 0, len(r.Use))
		for _, b := range r.Use {
			bindings = append(bindings, apiBindingConfigJSON{
				Agent:   b.Agent,
				Labels:  b.Labels,
				Cron:    b.Cron,
				Events:  b.Events,
				Enabled: b.IsEnabled(),
			})
		}
		repos = append(repos, apiRepoConfigJSON{
			Name:     r.Name,
			Enabled:  r.Enabled,
			Bindings: bindings,
		})
	}

	resp := apiConfigJSON{
		Backends: backends,
		Skills:   skills,
		Agents:   agents,
		Repos:    repos,
	}

	return json.Marshal(resp)
}

// ── /export and /import ──────────────────────────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded, it is not managed by the write API.
type exportYAML struct {
	Backends     map[string]fleet.Backend `yaml:"backends,omitempty"`
	Skills       map[string]fleet.Skill   `yaml:"skills,omitempty"`
	Agents       []fleet.Agent            `yaml:"agents,omitempty"`
	Repos        []fleet.Repo             `yaml:"repos,omitempty"`
	Guardrails   []fleet.Guardrail        `yaml:"guardrails,omitempty"`
	TokenBudgets []store.TokenBudget      `yaml:"token_budgets,omitempty"`
}

// HandleExport serves GET /export, returns a config.yaml fragment covering
// the CRUD-mutable sections (backends, skills, agents, repos,
// guardrails, token_budgets).
func (h *Handler) HandleExport(w http.ResponseWriter, _ *http.Request) {
	b, err := h.ExportYAML()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="config-export.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// ExportYAML returns the CRUD-mutable sections of the store as a YAML
// fragment matching the GET /export response body.
func (h *Handler) ExportYAML() ([]byte, error) {
	agents, repos, skills, backends, err := h.store.ReadSnapshot()
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	guardrails, err := h.store.ReadAllGuardrails()
	if err != nil {
		return nil, fmt.Errorf("read guardrails: %w", err)
	}
	budgets, err := h.store.ListTokenBudgets()
	if err != nil {
		return nil, fmt.Errorf("list token budgets: %w", err)
	}
	out := exportYAML{
		Backends:     backends,
		Skills:       skills,
		Agents:       agents,
		Repos:        repos,
		Guardrails:   guardrails,
		TokenBudgets: budgets,
	}
	b, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	return b, nil
}

// HandleImport serves POST /import, accepts a YAML body in the same format
// as HandleExport and upserts all entities into the DB. On success it
// returns 200 with a JSON summary of imported counts.
func (h *Handler) HandleImport(w http.ResponseWriter, r *http.Request) {
	limit := h.daemonCfg.HTTP.MaxBodyBytes * 10
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	counts, err := h.ImportYAML(body, r.URL.Query().Get("mode"))
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(counts)
}

// ImportYAML parses a YAML payload in HandleExport's format and writes it to
// the store atomically. mode controls upsert semantics: empty or "merge"
// preserves existing records, "replace" prunes anything not present in the
// payload. All sections — agents, repos, skills, backends, guardrails, and
// token budgets — are written in a single transaction so a failure in any
// section rolls back the entire import.
//
// On success it returns the per-section counts.
func (h *Handler) ImportYAML(body []byte, mode string) (map[string]int, error) {
	if mode != "" && mode != "merge" && mode != "replace" {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("invalid mode %q: must be empty, \"merge\", or \"replace\"", mode)}
	}
	var payload exportYAML
	if err := yaml.Unmarshal(body, &payload); err != nil {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("parse yaml: %v", err)}
	}

	var err error
	if mode == "replace" {
		err = h.store.ReplaceAll(payload.Agents, payload.Repos, payload.Skills, payload.Backends, payload.Guardrails, payload.TokenBudgets)
	} else {
		err = h.store.ImportAll(payload.Agents, payload.Repos, payload.Skills, payload.Backends, payload.Guardrails, payload.TokenBudgets)
	}
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}

	return map[string]int{
		"agents":        len(payload.Agents),
		"skills":        len(payload.Skills),
		"repos":         len(payload.Repos),
		"backends":      len(payload.Backends),
		"guardrails":    len(payload.Guardrails),
		"token_budgets": len(payload.TokenBudgets),
	}, nil
}

// storeErrStatus maps a store error to an HTTP status. Validation and
// not-found errors surface as 400 and 404; conflict errors as 409;
// everything else as 500.
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
