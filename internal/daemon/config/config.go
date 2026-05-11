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
	"strings"

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

// apiConfigJSON is the workspace-aware fleet-only wire shape for /config.
type apiConfigJSON struct {
	Backends     map[string]apiAIBackendConfigJSON `json:"backends,omitempty"`
	Prompts      []fleet.Prompt                    `json:"prompts,omitempty"`
	Skills       map[string]apiSkillJSON           `json:"skills,omitempty"`
	Guardrails   []fleet.Guardrail                 `json:"guardrails,omitempty"`
	Workspaces   []apiWorkspaceConfigJSON          `json:"workspaces,omitempty"`
	Agents       []apiAgentConfigJSON              `json:"agents,omitempty"`
	Repos        []apiRepoConfigJSON               `json:"repos,omitempty"`
	TokenBudgets []store.TokenBudget               `json:"token_budgets,omitempty"`
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
	WorkspaceID string                 `json:"workspace_id,omitempty"`
	Name        string                 `json:"name"`
	Enabled     bool                   `json:"enabled"`
	Bindings    []apiBindingConfigJSON `json:"bindings,omitempty"`
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
	ID          string `json:"id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Name        string `json:"name,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

type apiWorkspaceConfigJSON struct {
	ID          string                        `json:"id"`
	Name        string                        `json:"name"`
	Description string                        `json:"description,omitempty"`
	Guardrails  []fleet.WorkspaceGuardrailRef `json:"guardrails,omitempty"`
}

type apiAgentConfigJSON struct {
	ID            string   `json:"id,omitempty"`
	WorkspaceID   string   `json:"workspace_id,omitempty"`
	Name          string   `json:"name"`
	Backend       string   `json:"backend,omitempty"`
	Model         string   `json:"model,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	PromptID      string   `json:"prompt_id,omitempty"`
	PromptRef     string   `json:"prompt_ref,omitempty"`
	ScopeType     string   `json:"scope_type,omitempty"`
	ScopeRepo     string   `json:"scope_repo,omitempty"`
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
	cfg, err := h.store.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	budgets, err := h.store.ListTokenBudgets()
	if err != nil {
		return nil, fmt.Errorf("list token budgets: %w", err)
	}
	backends := make(map[string]apiAIBackendConfigJSON, len(cfg.Backends))
	for name, b := range cfg.Backends {
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

	skills := make(map[string]apiSkillJSON, len(cfg.Skills))
	for name, skill := range cfg.Skills {
		skills[name] = apiSkillJSON{
			ID:          skill.ID,
			WorkspaceID: skill.WorkspaceID,
			Repo:        skill.Repo,
			Name:        skill.Name,
			Prompt:      skill.Prompt,
		}
	}

	workspaces := make([]apiWorkspaceConfigJSON, 0, len(cfg.Workspaces))
	for _, w := range cfg.Workspaces {
		workspaces = append(workspaces, apiWorkspaceConfigJSON{
			ID:          w.ID,
			Name:        w.Name,
			Description: w.Description,
			Guardrails:  w.Guardrails,
		})
	}

	agents := make([]apiAgentConfigJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents = append(agents, apiAgentConfigJSON{
			ID:            a.ID,
			WorkspaceID:   a.WorkspaceID,
			Name:          a.Name,
			Backend:       a.Backend,
			Model:         a.Model,
			Skills:        a.Skills,
			PromptID:      a.PromptID,
			PromptRef:     a.PromptRef,
			ScopeType:     a.ScopeType,
			ScopeRepo:     a.ScopeRepo,
			Description:   a.Description,
			AllowPRs:      a.AllowPRs,
			AllowDispatch: a.AllowDispatch,
			AllowMemory:   a.IsAllowMemory(),
			CanDispatch:   a.CanDispatch,
		})
	}

	repos := make([]apiRepoConfigJSON, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
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
			WorkspaceID: r.WorkspaceID,
			Name:        r.Name,
			Enabled:     r.Enabled,
			Bindings:    bindings,
		})
	}

	resp := apiConfigJSON{
		Backends:     backends,
		Prompts:      cfg.Prompts,
		Skills:       skills,
		Guardrails:   cfg.Guardrails,
		Workspaces:   workspaces,
		Agents:       agents,
		Repos:        repos,
		TokenBudgets: budgets,
	}

	return json.Marshal(resp)
}

// ── /export and /import ──────────────────────────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded, it is not managed by the write API.
type exportYAML struct {
	Backends     map[string]fleet.Backend `yaml:"backends,omitempty"`
	Prompts      []fleet.Prompt           `yaml:"prompts,omitempty"`
	Skills       map[string]fleet.Skill   `yaml:"skills,omitempty"`
	Guardrails   []fleet.Guardrail        `yaml:"guardrails,omitempty"`
	Workspaces   []workspaceYAML          `yaml:"workspaces,omitempty"`
	Agents       []fleet.Agent            `yaml:"agents,omitempty"`        // legacy top-level import
	Repos        []fleet.Repo             `yaml:"repos,omitempty"`         // legacy top-level import
	TokenBudgets []store.TokenBudget      `yaml:"token_budgets,omitempty"` // legacy top-level import
}

type workspaceYAML struct {
	ID           string                        `yaml:"id,omitempty"`
	Name         string                        `yaml:"name"`
	Description  string                        `yaml:"description,omitempty"`
	Guardrails   []fleet.WorkspaceGuardrailRef `yaml:"guardrails,omitempty"`
	Agents       []fleet.Agent                 `yaml:"agents,omitempty"`
	Repos        []fleet.Repo                  `yaml:"repos,omitempty"`
	TokenBudgets []store.TokenBudget           `yaml:"token_budgets,omitempty"`
}

// HandleExport serves GET /export, returning the workspace-aware config.yaml
// fragment for global catalog assets and workspace-local fleet wiring.
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
	cfg, err := h.store.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	budgets, err := h.store.ListTokenBudgets()
	if err != nil {
		return nil, fmt.Errorf("list token budgets: %w", err)
	}

	workspaces := make([]workspaceYAML, 0, len(cfg.Workspaces))
	byWorkspace := make(map[string]int, len(cfg.Workspaces))
	for _, w := range cfg.Workspaces {
		workspaceID := fleet.NormalizeWorkspaceID(w.ID)
		byWorkspace[workspaceID] = len(workspaces)
		workspaces = append(workspaces, workspaceYAML{
			ID:          workspaceID,
			Name:        w.Name,
			Description: w.Description,
			Guardrails:  w.Guardrails,
		})
	}
	ensureWorkspace := func(workspaceID string) int {
		workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
		if idx, ok := byWorkspace[workspaceID]; ok {
			return idx
		}
		byWorkspace[workspaceID] = len(workspaces)
		workspaces = append(workspaces, workspaceYAML{ID: workspaceID, Name: workspaceID})
		return len(workspaces) - 1
	}
	for _, a := range cfg.Agents {
		idx := ensureWorkspace(a.WorkspaceID)
		a.WorkspaceID = ""
		a.Prompt = ""
		a.PromptID = ""
		workspaces[idx].Agents = append(workspaces[idx].Agents, a)
	}
	for _, r := range cfg.Repos {
		idx := ensureWorkspace(r.WorkspaceID)
		r.WorkspaceID = ""
		workspaces[idx].Repos = append(workspaces[idx].Repos, r)
	}
	for _, b := range budgets {
		if b.WorkspaceID == "" {
			continue
		}
		idx := ensureWorkspace(b.WorkspaceID)
		b.WorkspaceID = ""
		workspaces[idx].TokenBudgets = append(workspaces[idx].TokenBudgets, b)
	}
	var globalBudgets []store.TokenBudget
	for _, b := range budgets {
		if b.WorkspaceID == "" {
			globalBudgets = append(globalBudgets, b)
		}
	}
	out := exportYAML{
		Backends:     cfg.Backends,
		Prompts:      cfg.Prompts,
		Skills:       cfg.Skills,
		Guardrails:   cfg.Guardrails,
		Workspaces:   workspaces,
		TokenBudgets: globalBudgets,
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
// the store atomically. Legacy top-level agents/repos/token_budgets are still
// accepted and imported into their explicit workspace_id or Default.
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

	cfg := payload.toConfig()
	budgets := payload.flattenTokenBudgets()
	var err error
	if mode == "replace" {
		err = h.store.ReplaceConfig(cfg, budgets)
	} else {
		err = h.store.ImportConfig(cfg, budgets)
	}
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}

	return map[string]int{
		"agents":        len(cfg.Agents),
		"workspaces":    len(cfg.Workspaces),
		"prompts":       len(cfg.Prompts),
		"skills":        len(cfg.Skills),
		"repos":         len(cfg.Repos),
		"backends":      len(cfg.Backends),
		"guardrails":    len(cfg.Guardrails),
		"token_budgets": len(budgets),
	}, nil
}

func (p exportYAML) toConfig() *config.Config {
	cfg := &config.Config{
		Backends:   p.Backends,
		Prompts:    p.Prompts,
		Skills:     p.Skills,
		Guardrails: p.Guardrails,
		Agents:     append([]fleet.Agent{}, p.Agents...),
		Repos:      append([]fleet.Repo{}, p.Repos...),
	}
	for _, w := range p.Workspaces {
		workspaceID := workspaceYAMLID(w)
		cfg.Workspaces = append(cfg.Workspaces, fleet.Workspace{
			ID:          workspaceID,
			Name:        w.Name,
			Description: w.Description,
			Guardrails:  w.Guardrails,
		})
		for _, a := range w.Agents {
			if a.WorkspaceID == "" {
				a.WorkspaceID = workspaceID
			}
			cfg.Agents = append(cfg.Agents, a)
		}
		for _, r := range w.Repos {
			if r.WorkspaceID == "" {
				r.WorkspaceID = workspaceID
			}
			cfg.Repos = append(cfg.Repos, r)
		}
	}
	return cfg
}

func (p exportYAML) flattenTokenBudgets() []store.TokenBudget {
	budgets := append([]store.TokenBudget{}, p.TokenBudgets...)
	for _, w := range p.Workspaces {
		workspaceID := workspaceYAMLID(w)
		for _, b := range w.TokenBudgets {
			if b.WorkspaceID == "" {
				b.WorkspaceID = workspaceID
			}
			budgets = append(budgets, b)
		}
	}
	return budgets
}

func workspaceYAMLID(w workspaceYAML) string {
	if strings.TrimSpace(w.ID) != "" {
		return fleet.NormalizeWorkspaceID(w.ID)
	}
	id := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(w.Name), " ", "-"))
	return fleet.NormalizeWorkspaceID(id)
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
