package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/eloylp/agents/internal/server"
)

// ── /api/agents ───────────────────────────────────────────────────────────────

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
	Model         string             `json:"model,omitempty"`
	Skills        []string           `json:"skills,omitempty"`
	Description   string             `json:"description,omitempty"`
	AllowDispatch bool               `json:"allow_dispatch"`
	CanDispatch   []string           `json:"can_dispatch,omitempty"`
	AllowPRs      bool               `json:"allow_prs"`
	AllowMemory   bool               `json:"allow_memory"`
	CurrentStatus string             `json:"current_status"` // "running" | "idle"
	Bindings      []agentBindingJSON `json:"bindings,omitempty"`
}

// handleAPIAgents serves GET /api/agents — a fleet snapshot combining agent
// definitions from config with scheduling state from the StatusProvider.
func (s *Server) handleAPIAgents(w http.ResponseWriter, _ *http.Request) {
	// Index scheduling state by (agent, repo) for O(1) lookup below.
	scheduleByKey := map[string]server.AgentStatus{}
	if s.provider != nil {
		for _, st := range s.provider.AgentStatuses() {
			scheduleByKey[st.Name+"\x00"+st.Repo] = st
		}
	}

	cfg := s.loadCfg()

	// Build one entry per configured agent.
	agents := make([]apiAgentJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		currentStatus := "idle"
		if s.runtimeState != nil && s.runtimeState.IsRunning(a.Name) {
			currentStatus = "running"
		}
		entry := apiAgentJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Model:         a.Model,
			Skills:        a.Skills,
			Description:   a.Description,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
			AllowPRs:      a.AllowPRs,
			AllowMemory:   a.IsAllowMemory(),
			CurrentStatus: currentStatus,
		}

		// Collect bindings from all repos that reference this agent.
		// Disabled repos are excluded entirely — they are not active in the
		// runtime, so they should not appear in the fleet snapshot.
		for _, repo := range cfg.Repos {
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

// ── /api/config ───────────────────────────────────────────────────────────────

// apiConfigJSON is the wire shape for /api/config with secrets redacted.
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

// apiBindingConfigJSON is the wire shape for a repo binding in /api/config.
// Enabled is always an explicit bool: a nil *bool in config (meaning "default
// enabled") is normalized to true so clients see the effective value.
type apiBindingConfigJSON struct {
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Events  []string `json:"events,omitempty"`
	Enabled bool     `json:"enabled"`
}

// apiRepoConfigJSON is the wire shape for one repo in /api/config.
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

// handleAPIConfig serves GET /api/config — the effective parsed config with
// secret values replaced by "[redacted]". Env-var names are preserved so
// operators can identify which environment variable holds a given secret.
func (s *Server) handleAPIConfig(w http.ResponseWriter, _ *http.Request) {
	body, err := s.ConfigJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// ConfigJSON returns the effective parsed config as JSON bytes with secrets
// redacted. Exposed so surfaces beyond HTTP (e.g. the MCP get_config tool) can
// reuse the exact same wire shape without going through the router.
func (s *Server) ConfigJSON() ([]byte, error) {
	cfg := s.loadCfg()

	httpCfg := apiHTTPConfigJSON{
		ListenAddr:             cfg.Daemon.HTTP.ListenAddr,
		StatusPath:             cfg.Daemon.HTTP.StatusPath,
		WebhookPath:            cfg.Daemon.HTTP.WebhookPath,
		WebhookSecretEnv:       cfg.Daemon.HTTP.WebhookSecretEnv,
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
	backends := make(map[string]apiAIBackendConfigJSON, len(cfg.Daemon.AIBackends))
	for name, b := range cfg.Daemon.AIBackends {
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
		Enabled: cfg.Daemon.Proxy.Enabled,
		Path:    cfg.Daemon.Proxy.Path,
		Upstream: apiProxyUpstreamJSON{
			URL:            cfg.Daemon.Proxy.Upstream.URL,
			Model:          cfg.Daemon.Proxy.Upstream.Model,
			APIKeyEnv:      cfg.Daemon.Proxy.Upstream.APIKeyEnv,
			TimeoutSeconds: cfg.Daemon.Proxy.Upstream.TimeoutSeconds,
			// ExtraBody is not copied: see apiProxyUpstreamJSON comment.
		},
	}
	if cfg.Daemon.Proxy.Upstream.APIKey != "" {
		proxy.Upstream.APIKey = redacted
	}

	skills := make(map[string]apiSkillJSON, len(cfg.Skills))
	for name, skill := range cfg.Skills {
		skills[name] = apiSkillJSON{Prompt: skill.Prompt}
	}

	agents := make([]apiAgentConfigJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
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
			Name:     r.Name,
			Enabled:  r.Enabled,
			Bindings: bindings,
		})
	}

	resp := apiConfigJSON{
		Daemon: apiDaemonJSON{
			Log: apiLogConfigJSON{
				Level:  cfg.Daemon.Log.Level,
				Format: cfg.Daemon.Log.Format,
			},
			HTTP: httpCfg,
			Processor: apiProcessorConfigJSON{
				EventQueueBuffer:    cfg.Daemon.Processor.EventQueueBuffer,
				MaxConcurrentAgents: cfg.Daemon.Processor.MaxConcurrentAgents,
				Dispatch: apiDispatchConfigJSON{
					MaxDepth:           cfg.Daemon.Processor.Dispatch.MaxDepth,
					MaxFanout:          cfg.Daemon.Processor.Dispatch.MaxFanout,
					DedupWindowSeconds: cfg.Daemon.Processor.Dispatch.DedupWindowSeconds,
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
