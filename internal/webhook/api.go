package webhook

import (
	"encoding/json"
	"net/http"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// ── /api/agents ────────────────────────────────────────────────────────────

// agentScheduleJSON carries scheduling state for cron-backed agents.
type agentScheduleJSON struct {
	LastRun    *string `json:"last_run,omitempty"` // RFC3339 or omitted
	NextRun    string  `json:"next_run"`           // RFC3339
	LastStatus string  `json:"last_status,omitempty"`
}

// agentBindingJSON is the wire shape for one agent-to-repo binding.
// Schedule is populated only for cron bindings that have scheduling state.
type agentBindingJSON struct {
	Repo     string             `json:"repo"`
	Labels   []string           `json:"labels,omitempty"`
	Events   []string           `json:"events,omitempty"`
	Cron     string             `json:"cron,omitempty"`
	Enabled  bool               `json:"enabled"`
	Schedule *agentScheduleJSON `json:"schedule,omitempty"`
}

// apiAgentJSON is the wire shape for one agent in /api/agents.
type apiAgentJSON struct {
	Name          string             `json:"name"`
	Backend       string             `json:"backend"`
	Skills        []string           `json:"skills,omitempty"`
	Description   string             `json:"description,omitempty"`
	AllowDispatch bool               `json:"allow_dispatch"`
	CanDispatch   []string           `json:"can_dispatch,omitempty"`
	AllowPRs      bool               `json:"allow_prs"`
	Bindings      []agentBindingJSON `json:"bindings,omitempty"`
}

// handleAPIAgents serves GET /api/agents — a fleet snapshot combining agent
// definitions from config with scheduling state from the StatusProvider.
func (s *Server) handleAPIAgents(w http.ResponseWriter, _ *http.Request) {
	// Index scheduling state by (agent, repo) for O(1) lookup below.
	scheduleByKey := map[string]AgentStatus{}
	if s.provider != nil {
		for _, st := range s.provider.AgentStatuses() {
			scheduleByKey[st.Name+"\x00"+st.Repo] = st
		}
	}

	// Build one entry per configured agent.
	agents := make([]apiAgentJSON, 0, len(s.cfg.Agents))
	for _, a := range s.cfg.Agents {
		entry := apiAgentJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Skills:        a.Skills,
			Description:   a.Description,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
			AllowPRs:      a.AllowPRs,
		}

		// Collect bindings from all repos that reference this agent.
		// Disabled repos are excluded entirely — they are not active in the
		// runtime, so they should not appear in the fleet snapshot.
		for _, repo := range s.cfg.Repos {
			if !repo.Enabled {
				continue
			}
			for _, b := range repo.Use {
				if b.Agent != a.Name {
					continue
				}
				binding := agentBindingJSON{
					Repo:    repo.Name,
					Labels:  b.Labels,
					Events:  b.Events,
					Cron:    b.Cron,
					Enabled: b.IsEnabled(),
				}
				// Attach scheduling state onto the binding so agents with cron
				// schedules in multiple repos each carry their own schedule data.
				if b.IsCron() {
					if st, ok := scheduleByKey[a.Name+"\x00"+repo.Name]; ok {
						j := &agentScheduleJSON{
							NextRun:    st.NextRun.UTC().Format("2006-01-02T15:04:05Z"),
							LastStatus: st.LastStatus,
						}
						if st.LastRun != nil {
							lr := st.LastRun.UTC().Format("2006-01-02T15:04:05Z")
							j.LastRun = &lr
						}
						binding.Schedule = j
					}
				}
				entry.Bindings = append(entry.Bindings, binding)
			}
		}

		agents = append(agents, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agents)
}

// ── /api/config ────────────────────────────────────────────────────────────

// apiConfigJSON is the wire shape for /api/config with secrets redacted.
// Secrets (resolved values of *_env fields) are replaced with "[redacted]".
type apiConfigJSON struct {
	Daemon   apiDaemonJSON              `json:"daemon"`
	Skills   map[string]apiSkillJSON    `json:"skills,omitempty"`
	Agents   []apiAgentConfigJSON       `json:"agents,omitempty"`
	Repos    []config.RepoDef           `json:"repos,omitempty"`
}

type apiDaemonJSON struct {
	Log        config.LogConfig                   `json:"log"`
	HTTP       apiHTTPConfigJSON                  `json:"http"`
	Processor  config.ProcessorConfig             `json:"processor"`
	MemoryDir  string                             `json:"memory_dir,omitempty"`
	AIBackends map[string]apiAIBackendConfigJSON  `json:"ai_backends,omitempty"`
	Proxy      apiProxyConfigJSON                 `json:"proxy"`
}

type apiHTTPConfigJSON struct {
	ListenAddr             string `json:"listen_addr"`
	StatusPath             string `json:"status_path"`
	WebhookPath            string `json:"webhook_path"`
	AgentsRunPath          string `json:"agents_run_path"`
	WebhookSecretEnv       string `json:"webhook_secret_env,omitempty"`
	WebhookSecret          string `json:"webhook_secret,omitempty"` // always "[redacted]" when set
	APIKeyEnv              string `json:"api_key_env,omitempty"`
	APIKey                 string `json:"api_key,omitempty"` // always "[redacted]" when set
	ReadTimeoutSeconds     int    `json:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `json:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `json:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `json:"max_body_bytes"`
	DeliveryTTLSeconds     int    `json:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
}

type apiAIBackendConfigJSON struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"` // values are "[redacted]"
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MaxPromptChars   int               `json:"max_prompt_chars"`
	RedactionSaltEnv string            `json:"redaction_salt_env,omitempty"`
}

type apiProxyConfigJSON struct {
	Enabled  bool                   `json:"enabled"`
	Path     string                 `json:"path,omitempty"`
	Upstream apiProxyUpstreamJSON   `json:"upstream,omitempty"`
}

type apiProxyUpstreamJSON struct {
	URL            string         `json:"url,omitempty"`
	Model          string         `json:"model,omitempty"`
	APIKeyEnv      string         `json:"api_key_env,omitempty"`
	APIKey         string         `json:"api_key,omitempty"` // always "[redacted]" when set
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	ExtraBody      map[string]any `json:"extra_body,omitempty"`
}

type apiSkillJSON struct {
	PromptFile string `json:"prompt_file,omitempty"`
	// Prompt body is intentionally omitted: it can be very long.
}

type apiAgentConfigJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	PromptFile    string   `json:"prompt_file,omitempty"`
	Description   string   `json:"description,omitempty"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	CanDispatch   []string `json:"can_dispatch,omitempty"`
}

const redacted = "[redacted]"

// handleAPIConfig serves GET /api/config — the effective parsed config with
// secret values replaced by "[redacted]". Env-var names are preserved so
// operators can identify which environment variable holds a given secret.
func (s *Server) handleAPIConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.cfg

	httpCfg := apiHTTPConfigJSON{
		ListenAddr:             cfg.Daemon.HTTP.ListenAddr,
		StatusPath:             cfg.Daemon.HTTP.StatusPath,
		WebhookPath:            cfg.Daemon.HTTP.WebhookPath,
		AgentsRunPath:          cfg.Daemon.HTTP.AgentsRunPath,
		WebhookSecretEnv:       cfg.Daemon.HTTP.WebhookSecretEnv,
		APIKeyEnv:              cfg.Daemon.HTTP.APIKeyEnv,
		ReadTimeoutSeconds:     cfg.Daemon.HTTP.ReadTimeoutSeconds,
		WriteTimeoutSeconds:    cfg.Daemon.HTTP.WriteTimeoutSeconds,
		IdleTimeoutSeconds:     cfg.Daemon.HTTP.IdleTimeoutSeconds,
		MaxBodyBytes:           cfg.Daemon.HTTP.MaxBodyBytes,
		DeliveryTTLSeconds:     cfg.Daemon.HTTP.DeliveryTTLSeconds,
		ShutdownTimeoutSeconds: cfg.Daemon.HTTP.ShutdownTimeoutSeconds,
	}
	if cfg.Daemon.HTTP.WebhookSecret != "" {
		httpCfg.WebhookSecret = redacted
	}
	if cfg.Daemon.HTTP.APIKey != "" {
		httpCfg.APIKey = redacted
	}

	backends := make(map[string]apiAIBackendConfigJSON, len(cfg.Daemon.AIBackends))
	for name, b := range cfg.Daemon.AIBackends {
		redactedEnv := make(map[string]string, len(b.Env))
		for k := range b.Env {
			redactedEnv[k] = redacted
		}
		backends[name] = apiAIBackendConfigJSON{
			Command:          b.Command,
			Args:             b.Args,
			Env:              redactedEnv,
			TimeoutSeconds:   b.TimeoutSeconds,
			MaxPromptChars:   b.MaxPromptChars,
			RedactionSaltEnv: b.RedactionSaltEnv,
		}
	}

	proxy := apiProxyConfigJSON{
		Enabled: cfg.Daemon.Proxy.Enabled,
		Path:    cfg.Daemon.Proxy.Path,
		Upstream: apiProxyUpstreamJSON{
			URL:            cfg.Daemon.Proxy.Upstream.URL,
			Model:          cfg.Daemon.Proxy.Upstream.Model,
			APIKeyEnv:      cfg.Daemon.Proxy.Upstream.APIKeyEnv,
			TimeoutSeconds: cfg.Daemon.Proxy.Upstream.TimeoutSeconds,
			ExtraBody:      cfg.Daemon.Proxy.Upstream.ExtraBody,
		},
	}
	if cfg.Daemon.Proxy.Upstream.APIKey != "" {
		proxy.Upstream.APIKey = redacted
	}

	skills := make(map[string]apiSkillJSON, len(cfg.Skills))
	for name, skill := range cfg.Skills {
		skills[name] = apiSkillJSON{PromptFile: skill.PromptFile}
	}

	agents := make([]apiAgentConfigJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents = append(agents, apiAgentConfigJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Skills:        a.Skills,
			PromptFile:    a.PromptFile,
			Description:   a.Description,
			AllowPRs:      a.AllowPRs,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
		})
	}

	resp := apiConfigJSON{
		Daemon: apiDaemonJSON{
			Log:        cfg.Daemon.Log,
			HTTP:       httpCfg,
			Processor:  cfg.Daemon.Processor,
			MemoryDir:  cfg.Daemon.MemoryDir,
			AIBackends: backends,
			Proxy:      proxy,
		},
		Skills: skills,
		Agents: agents,
		Repos:  cfg.Repos,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ── /api/dispatches ────────────────────────────────────────────────────────

// handleAPIDispatches serves GET /api/dispatches — the current dispatch
// counters as reported by the DispatchStatsProvider. Returns an empty object
// when no provider is configured (e.g. no dispatch configured).
func (s *Server) handleAPIDispatches(w http.ResponseWriter, _ *http.Request) {
	var stats workflow.DispatchStats
	if s.dispatchStats != nil {
		stats = s.dispatchStats.DispatchStats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}
