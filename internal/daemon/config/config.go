// Package config implements the /config, /export, and /import HTTP surface
// plus the methods the MCP get_config / export_config / import_config tools
// call directly. Both surfaces share one canonical wire shape (the
// apiConfigJSON tree for /config; the exportYAML fragment for /export +
// /import) so REST and MCP clients see identical payloads.
//
// The handler reads the four CRUD-mutable entity sets from SQLite on every
// request; the static daemon-level config (HTTP, proxy, log, processor) is
// captured at construction since those never mutate via CRUD.
package config

import (
	"database/sql"
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
	db        *sql.DB
	daemonCfg config.DaemonConfig
	logger    zerolog.Logger
}

// New constructs a Handler. db is read on every request to assemble the
// /config response from the latest committed entities. daemonCfg supplies
// the static daemon-level fields the response embeds.
func New(db *sql.DB, daemonCfg config.DaemonConfig, logger zerolog.Logger) *Handler {
	return &Handler{
		db:        db,
		daemonCfg: daemonCfg,
		logger:    logger.With().Str("component", "server_config").Logger(),
	}
}

// RegisterRoutes mounts the config endpoints on r.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/config", withTimeout(http.HandlerFunc(h.HandleConfig))).Methods(http.MethodGet)
	r.Handle("/export", withTimeout(http.HandlerFunc(h.HandleExport))).Methods(http.MethodGet)
	r.Handle("/import", withTimeout(http.HandlerFunc(h.HandleImport))).Methods(http.MethodPost)
}

// ── /config ─────────────────────────────────────────────────────────────────

// apiConfigJSON is the wire shape for /config with secrets redacted.
// Secrets (resolved values of *_env fields) are replaced with "[redacted]".
type apiConfigJSON struct {
	Daemon apiDaemonJSON           `json:"daemon"`
	Skills map[string]apiSkillJSON `json:"skills,omitempty"`
	Agents []apiAgentConfigJSON    `json:"agents,omitempty"`
	Repos  []apiRepoConfigJSON     `json:"repos,omitempty"`
}

type apiDaemonJSON struct {
	Log        apiLogConfigJSON                  `json:"log"`
	HTTP       apiHTTPConfigJSON                 `json:"http"`
	Processor  apiProcessorConfigJSON            `json:"processor"`
	AIBackends map[string]apiAIBackendConfigJSON `json:"ai_backends,omitempty"`
	Proxy      apiProxyConfigJSON                `json:"proxy"`
}

type apiLogConfigJSON struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type apiDispatchConfigJSON struct {
	MaxDepth           int `json:"max_depth"`
	MaxFanout          int `json:"max_fanout"`
	DedupWindowSeconds int `json:"dedup_window_seconds"`
}

type apiProcessorConfigJSON struct {
	EventQueueBuffer    int                   `json:"event_queue_buffer"`
	MaxConcurrentAgents int                   `json:"max_concurrent_agents"`
	Dispatch            apiDispatchConfigJSON `json:"dispatch"`
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

type apiHTTPConfigJSON struct {
	ListenAddr             string `json:"listen_addr"`
	StatusPath             string `json:"status_path"`
	WebhookPath            string `json:"webhook_path"`
	WebhookSecretEnv       string `json:"webhook_secret_env,omitempty"`
	WebhookSecret          string `json:"webhook_secret,omitempty"` // always "[redacted]" when set
	ReadTimeoutSeconds     int    `json:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `json:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `json:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `json:"max_body_bytes"`
	DeliveryTTLSeconds     int    `json:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
}

type apiAIBackendConfigJSON struct {
	Command          string   `json:"command"`
	Version          string   `json:"version,omitempty"`
	Models           []string `json:"models,omitempty"`
	Healthy          bool     `json:"healthy"`
	HealthDetail     string   `json:"health_detail,omitempty"`
	LocalModelURL    string   `json:"local_model_url,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	MaxPromptChars   int      `json:"max_prompt_chars"`
	RedactionSaltEnv string   `json:"redaction_salt_env,omitempty"`
}

type apiProxyConfigJSON struct {
	Enabled  bool                 `json:"enabled"`
	Path     string               `json:"path,omitempty"`
	Upstream apiProxyUpstreamJSON `json:"upstream,omitempty"`
}

type apiProxyUpstreamJSON struct {
	URL            string `json:"url,omitempty"`
	Model          string `json:"model,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	APIKey         string `json:"api_key,omitempty"` // always "[redacted]" when set
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// ExtraBody is intentionally omitted: values can contain bearer tokens or
	// other secrets and there is no way to safely distinguish them from safe
	// tuning knobs without domain knowledge of every possible upstream vendor.
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

const redacted = "[redacted]"

// HandleConfig serves GET /config — the effective parsed config with secret
// values replaced by "[redacted]". Env-var names are preserved so operators
// can identify which environment variable holds a given secret.
func (h *Handler) HandleConfig(w http.ResponseWriter, _ *http.Request) {
	body, err := h.ConfigJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ConfigJSON returns the effective parsed config as JSON bytes with secrets
// redacted. Exposed so surfaces beyond HTTP (e.g. the MCP get_config tool)
// can reuse the exact same wire shape without going through the router.
func (h *Handler) ConfigJSON() ([]byte, error) {
	dcfg := h.daemonCfg
	storedAgents, storedRepos, storedSkills, storedBackends, err := store.ReadSnapshot(h.db)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}

	httpCfg := apiHTTPConfigJSON{
		ListenAddr:             dcfg.HTTP.ListenAddr,
		StatusPath:             dcfg.HTTP.StatusPath,
		WebhookPath:            dcfg.HTTP.WebhookPath,
		WebhookSecretEnv:       dcfg.HTTP.WebhookSecretEnv,
		ReadTimeoutSeconds:     dcfg.HTTP.ReadTimeoutSeconds,
		WriteTimeoutSeconds:    dcfg.HTTP.WriteTimeoutSeconds,
		IdleTimeoutSeconds:     dcfg.HTTP.IdleTimeoutSeconds,
		MaxBodyBytes:           dcfg.HTTP.MaxBodyBytes,
		DeliveryTTLSeconds:     dcfg.HTTP.DeliveryTTLSeconds,
		ShutdownTimeoutSeconds: dcfg.HTTP.ShutdownTimeoutSeconds,
	}
	if dcfg.HTTP.WebhookSecret != "" {
		httpCfg.WebhookSecret = redacted
	}
	backends := make(map[string]apiAIBackendConfigJSON, len(storedBackends))
	for name, b := range storedBackends {
		backends[name] = apiAIBackendConfigJSON{
			Command:          b.Command,
			Version:          b.Version,
			Models:           b.Models,
			Healthy:          b.Healthy,
			HealthDetail:     b.HealthDetail,
			LocalModelURL:    b.LocalModelURL,
			TimeoutSeconds:   b.TimeoutSeconds,
			MaxPromptChars:   b.MaxPromptChars,
			RedactionSaltEnv: b.RedactionSaltEnv,
		}
	}

	proxy := apiProxyConfigJSON{
		Enabled: dcfg.Proxy.Enabled,
		Path:    dcfg.Proxy.Path,
		Upstream: apiProxyUpstreamJSON{
			URL:            dcfg.Proxy.Upstream.URL,
			Model:          dcfg.Proxy.Upstream.Model,
			APIKeyEnv:      dcfg.Proxy.Upstream.APIKeyEnv,
			TimeoutSeconds: dcfg.Proxy.Upstream.TimeoutSeconds,
			// ExtraBody is not copied: see apiProxyUpstreamJSON comment.
		},
	}
	if dcfg.Proxy.Upstream.APIKey != "" {
		proxy.Upstream.APIKey = redacted
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
		Daemon: apiDaemonJSON{
			Log: apiLogConfigJSON{
				Level:  dcfg.Log.Level,
				Format: dcfg.Log.Format,
			},
			HTTP: httpCfg,
			Processor: apiProcessorConfigJSON{
				EventQueueBuffer:    dcfg.Processor.EventQueueBuffer,
				MaxConcurrentAgents: dcfg.Processor.MaxConcurrentAgents,
				Dispatch: apiDispatchConfigJSON{
					MaxDepth:           dcfg.Processor.Dispatch.MaxDepth,
					MaxFanout:          dcfg.Processor.Dispatch.MaxFanout,
					DedupWindowSeconds: dcfg.Processor.Dispatch.DedupWindowSeconds,
				},
			},
			AIBackends: backends,
			Proxy:      proxy,
		},
		Skills: skills,
		Agents: agents,
		Repos:  repos,
	}

	return json.Marshal(resp)
}

// ── /export and /import ──────────────────────────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// four CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded — it is not managed by the write API.
type exportYAML struct {
	Skills map[string]fleet.Skill `yaml:"skills,omitempty"`
	Agents []fleet.Agent          `yaml:"agents,omitempty"`
	Repos  []fleet.Repo           `yaml:"repos,omitempty"`
	Daemon *exportDaemonYAML      `yaml:"daemon,omitempty"`
}

type exportDaemonYAML struct {
	AIBackends map[string]fleet.Backend `yaml:"ai_backends,omitempty"`
}

// HandleExport serves GET /export — returns a config.yaml fragment covering
// the four CRUD-mutable sections (skills, agents, repos, daemon.ai_backends).
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
	agents, repos, skills, backends, err := store.ReadSnapshot(h.db)
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

// HandleImport serves POST /import — accepts a YAML body in the same format
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
// the store. mode controls upsert semantics: empty or "merge" preserves
// existing records, "replace" prunes anything not present in the payload.
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

	backends := map[string]fleet.Backend{}
	if payload.Daemon != nil {
		backends = payload.Daemon.AIBackends
	}

	var err error
	if mode == "replace" {
		err = store.ReplaceAll(h.db, payload.Agents, payload.Repos, payload.Skills, backends)
	} else {
		err = store.ImportAll(h.db, payload.Agents, payload.Repos, payload.Skills, backends)
	}
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}

	return map[string]int{
		"agents":   len(payload.Agents),
		"skills":   len(payload.Skills),
		"repos":    len(payload.Repos),
		"backends": len(backends),
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
