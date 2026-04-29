// Package config defines the agents daemon configuration schema and loader.
//
// The config file is structured in three top-level sections:
//
//	daemon — how the service runs (logging, HTTP, queues, AI backends)
//	skills — reusable guidance blocks referenced by agents
//	agents — named capabilities (backend + skills + prompt)
//	repos  — wiring: which agents run on which repo, and when
//
// See config.example.yaml for a complete annotated example.
//
// The package is split across a handful of files by concern:
//
//	config.go             — types, Load / FinishLoad entry points, lookups
//	defaults.go           — applyDefaults, normalize, setDefault helpers
//	prompts.go            — prompt_file resolution + secret env-var resolution
//	validate.go           — internal validate* tree called from Config.validate()
//	validate_entities.go  — exported ValidateCrossRefs / ValidateEntities for the
//	                        SQLite CRUD layer (entity-level checks, no daemon section)
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/fleet"
)

// Config is the root configuration loaded from YAML.
type Config struct {
	Daemon DaemonConfig           `yaml:"daemon"`
	Skills map[string]fleet.Skill `yaml:"skills"`
	Agents []fleet.Agent          `yaml:"agents"`
	Repos  []fleet.Repo           `yaml:"repos"`

	// configDir is the directory containing the config file, used to resolve
	// prompt_file paths.
	configDir string `yaml:"-"`
}

// DaemonConfig holds infrastructure-level configuration for the running
// daemon. Nothing here is specific to any particular agent or repo.
type DaemonConfig struct {
	Log        LogConfig                `yaml:"log"`
	HTTP       HTTPConfig               `yaml:"http"`
	Processor  ProcessorConfig          `yaml:"processor"`
	AIBackends map[string]fleet.Backend `yaml:"ai_backends"`
	Proxy      ProxyConfig              `yaml:"proxy"`
}

// ProxyConfig controls the built-in Anthropic↔OpenAI translation proxy.
// When Enabled is false (the default) no additional route is mounted.
type ProxyConfig struct {
	Enabled  bool                `yaml:"enabled"`
	Path     string              `yaml:"path"`
	Upstream ProxyUpstreamConfig `yaml:"upstream"`
}

// ProxyUpstreamConfig describes the OpenAI-compatible endpoint the proxy
// forwards requests to.
type ProxyUpstreamConfig struct {
	URL            string         `yaml:"url"`
	Model          string         `yaml:"model"`
	APIKeyEnv      string         `yaml:"api_key_env"`
	TimeoutSeconds int            `yaml:"timeout_seconds"`
	ExtraBody      map[string]any `yaml:"extra_body"`

	// APIKey is resolved from APIKeyEnv at load time and is not present in YAML.
	APIKey string `yaml:"-"`
}

// LogConfig controls daemon logging output.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// HTTPConfig controls the daemon's HTTP server (webhooks + /status + /agents/run).
type HTTPConfig struct {
	ListenAddr             string `yaml:"listen_addr"`
	StatusPath             string `yaml:"status_path"`
	WebhookPath            string `yaml:"webhook_path"`
	WebhookSecretEnv       string `yaml:"webhook_secret_env"`
	ReadTimeoutSeconds     int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `yaml:"max_body_bytes"`
	DeliveryTTLSeconds     int    `yaml:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds"`

	// WebhookSecret is resolved from WebhookSecretEnv at load time
	// and not present in the YAML source.
	WebhookSecret string `yaml:"-"`
}

// ProcessorConfig controls the internal event queue and agent concurrency.
type ProcessorConfig struct {
	EventQueueBuffer    int            `yaml:"event_queue_buffer"`
	MaxConcurrentAgents int            `yaml:"max_concurrent_agents"`
	Dispatch            DispatchConfig `yaml:"dispatch"`
}

// DispatchConfig controls inter-agent dispatch safety limits.
type DispatchConfig struct {
	// MaxDepth is the maximum dispatch chain length; a chain longer than this
	// is dropped with a WARN log. Default: 3.
	MaxDepth int `yaml:"max_depth"`
	// MaxFanout caps how many dispatches a single agent run may enqueue.
	// Excess requests are dropped with a WARN log. Default: 4.
	MaxFanout int `yaml:"max_fanout"`
	// DedupWindowSeconds suppresses duplicate (target_agent, repo, number)
	// dispatch requests within the window. Default: 300.
	DedupWindowSeconds int `yaml:"dedup_window_seconds"`
}

// FinishLoad applies defaults, normalization, secret resolution, and
// validation to a Config that was populated by means other than Load (e.g.
// read from the SQLite store). It does NOT attempt to resolve prompt_file
// references — callers are expected to have already populated cfg.Agents and
// cfg.Skills with inline prompt text.
func FinishLoad(cfg *Config) (*Config, error) {
	cfg.applyDefaults()
	cfg.normalize()
	cfg.resolveSecrets()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Load reads, parses, validates, and resolves a config file at the given
// path. Prompt files referenced by PromptFile fields are read eagerly;
// any I/O or validation error is reported here.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg.configDir = filepath.Dir(abs)

	cfg.applyDefaults()
	cfg.normalize()
	cfg.resolveSecrets()
	if err := cfg.loadPromptFiles(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// RepoByName returns the repo definition with the given full name
// (case-insensitive).
func (c *Config) RepoByName(name string) (fleet.Repo, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, r := range c.Repos {
		if strings.ToLower(r.Name) == name {
			return r, true
		}
	}
	return fleet.Repo{}, false
}

// AgentByName returns the agent definition with the given name
// (case-insensitive).
func (c *Config) AgentByName(name string) (fleet.Agent, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// ResolveBackend returns the concrete backend name for the given agent
// configuration value. The backend must be explicitly configured; empty
// or unknown names return "".
func (c *Config) ResolveBackend(configured string) string {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == "" {
		return ""
	}
	if _, ok := c.Daemon.AIBackends[configured]; !ok {
		return ""
	}
	return configured
}
