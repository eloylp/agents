// Package config defines the agents daemon configuration schema and loader.
//
// The config file is structured in three top-level sections:
//
//   daemon — how the service runs (logging, HTTP, queues, AI backends)
//   skills — reusable guidance blocks referenced by agents
//   agents — named capabilities (backend + skills + prompt)
//   repos  — wiring: which agents run on which repo, and when
//
// See config.example.yaml for a complete annotated example.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// validAIBackendNames is the canonical ordered list of supported AI backend
// names. Preference order for the "auto" backend resolution follows slice
// order. Adding a new backend only requires updating this slice.
var validAIBackendNames = []string{"claude", "codex"}

// validEventKinds is the set of event kind strings accepted in the events:
// binding field. It must be kept in sync with the kinds emitted by the webhook
// server handlers.
var validEventKinds = map[string]struct{}{
	"issues.labeled":              {},
	"issues.opened":               {},
	"issues.edited":               {},
	"issues.reopened":             {},
	"issues.closed":               {},
	"pull_request.labeled":        {},
	"pull_request.opened":         {},
	"pull_request.synchronize":    {},
	"pull_request.ready_for_review": {},
	"pull_request.closed":         {},
	"issue_comment.created":       {},
	"pull_request_review.submitted":         {},
	"pull_request_review_comment.created":   {},
	"push":                                  {},
}

// validEventKindsSorted is a precomputed, sorted list of validEventKinds keys
// for use in human-readable error messages.
var validEventKindsSorted = func() []string {
	ks := make([]string, 0, len(validEventKinds))
	for k := range validEventKinds {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}()

const (
	defaultHTTPListenAddr          = ":8080"
	defaultHTTPStatusPath          = "/status"
	defaultHTTPWebhookPath         = "/webhooks/github"
	defaultHTTPAgentsRunPath       = "/agents/run"
	defaultHTTPReadTimeoutSeconds  = 15
	defaultHTTPWriteTimeoutSeconds = 15
	defaultHTTPIdleTimeoutSeconds  = 60
	defaultHTTPMaxBodyBytes        = 1 << 20
	defaultDeliveryTTLSeconds      = 3600
	defaultHTTPShutdownSeconds     = 15

	defaultEventQueueBufferSize = 256
	defaultMaxConcurrentAgents  = 4

	defaultAITimeoutSeconds = 600
	defaultMaxPromptChars   = 12000

	defaultMemoryDir = "/var/lib/agents/memory"

	defaultProxyPath           = "/v1/messages"
	defaultProxyTimeoutSeconds = 120
)

// Config is the root configuration loaded from YAML.
type Config struct {
	Daemon DaemonConfig        `yaml:"daemon"`
	Skills map[string]SkillDef `yaml:"skills"`
	Agents []AgentDef          `yaml:"agents"`
	Repos  []RepoDef           `yaml:"repos"`

	// configDir is the directory containing the config file, used to resolve
	// prompt_file paths.
	configDir string `yaml:"-"`
}

// DaemonConfig holds infrastructure-level configuration for the running
// daemon. Nothing here is specific to any particular agent or repo.
type DaemonConfig struct {
	Log        LogConfig                  `yaml:"log"`
	HTTP       HTTPConfig                 `yaml:"http"`
	Processor  ProcessorConfig            `yaml:"processor"`
	MemoryDir  string                     `yaml:"memory_dir"`
	AIBackends map[string]AIBackendConfig `yaml:"ai_backends"`
	Proxy      ProxyConfig                `yaml:"proxy"`
}

// ProxyConfig controls the built-in Anthropic↔OpenAI translation proxy.
// When Enabled is false (the default) no additional route is mounted.
type ProxyConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Path     string            `yaml:"path"`
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
	AgentsRunPath          string `yaml:"agents_run_path"`
	WebhookSecretEnv       string `yaml:"webhook_secret_env"`
	APIKeyEnv              string `yaml:"api_key_env"`
	ReadTimeoutSeconds     int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `yaml:"max_body_bytes"`
	DeliveryTTLSeconds     int    `yaml:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds"`

	// WebhookSecret and APIKey are resolved from the *Env fields at load time
	// and not present in the YAML source.
	WebhookSecret string `yaml:"-"`
	APIKey        string `yaml:"-"`
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

// AIBackendConfig describes how to invoke a CLI-based AI backend.
//
// Env is merged on top of the inherited subprocess environment (after the
// daemon's allowlist is applied). Typical use: route the claude CLI through
// a local OpenAI-compatible endpoint by setting
// ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY / ANTHROPIC_MODEL here, without
// touching the global container env.
type AIBackendConfig struct {
	Command          string            `yaml:"command"`
	Args             []string          `yaml:"args"`
	Env              map[string]string `yaml:"env"`
	TimeoutSeconds   int               `yaml:"timeout_seconds"`
	MaxPromptChars   int               `yaml:"max_prompt_chars"`
	RedactionSaltEnv string            `yaml:"redaction_salt_env"`
}

// SkillDef is a reusable block of guidance that agents can compose.
// After loading, Prompt always contains the resolved guidance text; PromptFile
// is retained only for debugging/logging.
type SkillDef struct {
	Prompt     string `yaml:"prompt"`
	PromptFile string `yaml:"prompt_file"`
}

// AgentDef is a named capability: a backend, a set of skills, and a prompt.
// Agents are pure definitions — they don't run on their own. Repos bind them
// to triggers.
type AgentDef struct {
	Name       string   `yaml:"name"`
	Backend    string   `yaml:"backend"`
	Skills     []string `yaml:"skills"`
	Prompt     string   `yaml:"prompt"`
	PromptFile string   `yaml:"prompt_file"`
	// AllowPRs controls whether the agent is permitted to open pull requests.
	// Defaults to false; the scheduler prepends a hard no-PR instruction when
	// false so the gate is code-level rather than relying on prompt wording.
	AllowPRs bool `yaml:"allow_prs"`

	// Description is a short human-readable summary of what this agent does.
	// Required when the agent appears in any other agent's can_dispatch list.
	Description string `yaml:"description"`

	// AllowDispatch opts this agent in as a dispatch target. Default false.
	// Other agents may only dispatch to this agent when this is true.
	AllowDispatch bool `yaml:"allow_dispatch"`

	// CanDispatch is the whitelist of agent names this agent is allowed to
	// dispatch. Validated: entries must reference real agents in the same
	// config and must not include the agent itself.
	CanDispatch []string `yaml:"can_dispatch"`
}

// RepoDef describes a single GitHub repo the daemon operates on and the
// agents bound to it.
type RepoDef struct {
	Name    string    `yaml:"name"`
	Enabled bool      `yaml:"enabled"`
	Use     []Binding `yaml:"use"`
}

// Binding wires an agent to one or more triggers on a specific repo.
// An agent can appear multiple times in a repo's Use list with different
// triggers.
type Binding struct {
	Agent   string   `yaml:"agent"`
	Labels  []string `yaml:"labels"`
	Cron    string   `yaml:"cron"`
	Events  []string `yaml:"events"`
	Enabled *bool    `yaml:"enabled"`
}

// IsEnabled reports whether this binding should be active. Absent =
// enabled; only explicit `enabled: false` disables.
func (b Binding) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}

// IsCron reports whether this binding is cron-triggered.
func (b Binding) IsCron() bool { return strings.TrimSpace(b.Cron) != "" }

// IsLabel reports whether this binding is label-triggered.
func (b Binding) IsLabel() bool { return len(b.Labels) > 0 }

// IsEvent reports whether this binding is event-triggered (via the events: field).
func (b Binding) IsEvent() bool { return len(b.Events) > 0 }

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
func (c *Config) RepoByName(name string) (RepoDef, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, r := range c.Repos {
		if strings.ToLower(r.Name) == name {
			return r, true
		}
	}
	return RepoDef{}, false
}

// AgentByName returns the agent definition with the given name
// (case-insensitive).
func (c *Config) AgentByName(name string) (AgentDef, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, a := range c.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return AgentDef{}, false
}

// DefaultBackend returns the first configured backend from validAIBackendNames.
// Used when an agent specifies backend: "auto" or leaves it empty.
func (c *Config) DefaultBackend() string {
	for _, name := range validAIBackendNames {
		if _, ok := c.Daemon.AIBackends[name]; ok {
			return name
		}
	}
	return ""
}

// ResolveBackend returns the concrete backend name for the given agent
// configuration value. "auto" or empty resolves to the default configured
// backend; an explicit name is returned as-is if it is present in
// ai_backends. Returns "" if the name is explicit but not configured.
func (c *Config) ResolveBackend(configured string) string {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == "" || configured == "auto" {
		return c.DefaultBackend()
	}
	if _, ok := c.Daemon.AIBackends[configured]; !ok {
		return ""
	}
	return configured
}

// ─── internal: defaults, normalization, secrets, prompt loading, validation ──

func (c *Config) applyDefaults() {
	// daemon.memory_dir
	if strings.TrimSpace(c.Daemon.MemoryDir) == "" {
		c.Daemon.MemoryDir = defaultMemoryDir
	}

	// daemon.http
	setDefault(&c.Daemon.HTTP.ListenAddr, defaultHTTPListenAddr)
	setDefault(&c.Daemon.HTTP.StatusPath, defaultHTTPStatusPath)
	setDefault(&c.Daemon.HTTP.WebhookPath, defaultHTTPWebhookPath)
	setDefault(&c.Daemon.HTTP.AgentsRunPath, defaultHTTPAgentsRunPath)
	setDefaultInt(&c.Daemon.HTTP.ReadTimeoutSeconds, defaultHTTPReadTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.WriteTimeoutSeconds, defaultHTTPWriteTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.IdleTimeoutSeconds, defaultHTTPIdleTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.MaxBodyBytes, defaultHTTPMaxBodyBytes)
	setDefaultInt(&c.Daemon.HTTP.DeliveryTTLSeconds, defaultDeliveryTTLSeconds)
	setDefaultInt(&c.Daemon.HTTP.ShutdownTimeoutSeconds, defaultHTTPShutdownSeconds)

	// daemon.processor
	setDefaultInt(&c.Daemon.Processor.EventQueueBuffer, defaultEventQueueBufferSize)
	setDefaultInt(&c.Daemon.Processor.MaxConcurrentAgents, defaultMaxConcurrentAgents)
	setDefaultInt(&c.Daemon.Processor.Dispatch.MaxDepth, 3)
	setDefaultInt(&c.Daemon.Processor.Dispatch.MaxFanout, 4)
	setDefaultInt(&c.Daemon.Processor.Dispatch.DedupWindowSeconds, 300)

	// daemon.proxy defaults (only applied when proxy is enabled or path is set)
	setDefault(&c.Daemon.Proxy.Path, defaultProxyPath)
	setDefaultInt(&c.Daemon.Proxy.Upstream.TimeoutSeconds, defaultProxyTimeoutSeconds)

	// daemon.ai_backends defaults
	for name, backend := range c.Daemon.AIBackends {
		if backend.TimeoutSeconds == 0 {
			backend.TimeoutSeconds = defaultAITimeoutSeconds
		}
		if backend.MaxPromptChars == 0 {
			backend.MaxPromptChars = defaultMaxPromptChars
		}
		c.Daemon.AIBackends[name] = backend
	}

	// agents: default backend to "auto" when empty
	for i := range c.Agents {
		if strings.TrimSpace(c.Agents[i].Backend) == "" {
			c.Agents[i].Backend = "auto"
		}
	}

	// repos: default enabled to true when field absent is ambiguous; YAML
	// zero-value is false. We leave it as-is — absent means false here,
	// because repos are an explicit allow-list.
}

func (c *Config) normalize() {
	// Lowercase backend keys for case-insensitive matching.
	if len(c.Daemon.AIBackends) > 0 {
		lower := make(map[string]AIBackendConfig, len(c.Daemon.AIBackends))
		for name, backend := range c.Daemon.AIBackends {
			key := strings.ToLower(strings.TrimSpace(name))
			backend.Command = strings.TrimSpace(backend.Command)
			if len(backend.Env) > 0 {
				cleaned := make(map[string]string, len(backend.Env))
				for k, v := range backend.Env {
					k = strings.TrimSpace(k)
					if k == "" {
						continue
					}
					cleaned[k] = v
				}
				backend.Env = cleaned
			}
			lower[key] = backend
		}
		c.Daemon.AIBackends = lower
	}

	// Lowercase skill keys.
	if len(c.Skills) > 0 {
		lower := make(map[string]SkillDef, len(c.Skills))
		for name, skill := range c.Skills {
			key := strings.ToLower(strings.TrimSpace(name))
			skill.Prompt = strings.TrimSpace(skill.Prompt)
			skill.PromptFile = strings.TrimSpace(skill.PromptFile)
			lower[key] = skill
		}
		c.Skills = lower
	}

	// Agents.
	for i := range c.Agents {
		c.Agents[i].Name = strings.ToLower(strings.TrimSpace(c.Agents[i].Name))
		c.Agents[i].Backend = strings.ToLower(strings.TrimSpace(c.Agents[i].Backend))
		c.Agents[i].Prompt = strings.TrimSpace(c.Agents[i].Prompt)
		c.Agents[i].PromptFile = strings.TrimSpace(c.Agents[i].PromptFile)
		c.Agents[i].Description = strings.TrimSpace(c.Agents[i].Description)
		for j := range c.Agents[i].Skills {
			c.Agents[i].Skills[j] = strings.ToLower(strings.TrimSpace(c.Agents[i].Skills[j]))
		}
		for j := range c.Agents[i].CanDispatch {
			c.Agents[i].CanDispatch[j] = strings.ToLower(strings.TrimSpace(c.Agents[i].CanDispatch[j]))
		}
	}

	// Repos.
	for i := range c.Repos {
		c.Repos[i].Name = strings.TrimSpace(c.Repos[i].Name)
		for j := range c.Repos[i].Use {
			c.Repos[i].Use[j].Agent = strings.ToLower(strings.TrimSpace(c.Repos[i].Use[j].Agent))
			c.Repos[i].Use[j].Cron = strings.TrimSpace(c.Repos[i].Use[j].Cron)
			for k := range c.Repos[i].Use[j].Events {
				c.Repos[i].Use[j].Events[k] = strings.ToLower(strings.TrimSpace(c.Repos[i].Use[j].Events[k]))
			}
		}
	}

	// Log.
	c.Daemon.Log.Level = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Level))
	c.Daemon.Log.Format = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Format))
}

func (c *Config) resolveSecrets() {
	if c.Daemon.HTTP.WebhookSecret == "" && c.Daemon.HTTP.WebhookSecretEnv != "" {
		c.Daemon.HTTP.WebhookSecret = os.Getenv(c.Daemon.HTTP.WebhookSecretEnv)
	}
	if c.Daemon.HTTP.APIKey == "" && c.Daemon.HTTP.APIKeyEnv != "" {
		c.Daemon.HTTP.APIKey = os.Getenv(c.Daemon.HTTP.APIKeyEnv)
	}
	if c.Daemon.Proxy.Upstream.APIKey == "" && c.Daemon.Proxy.Upstream.APIKeyEnv != "" {
		c.Daemon.Proxy.Upstream.APIKey = os.Getenv(c.Daemon.Proxy.Upstream.APIKeyEnv)
	}
}

// loadPromptFiles reads any prompt_file references in skills and agents,
// populating the Prompt field with the resolved content. Paths are resolved
// relative to the config file's directory.
func (c *Config) loadPromptFiles() error {
	for name, skill := range c.Skills {
		content, err := c.resolvePrompt("skill "+name, skill.Prompt, skill.PromptFile)
		if err != nil {
			return err
		}
		skill.Prompt = content
		c.Skills[name] = skill
	}
	for i := range c.Agents {
		content, err := c.resolvePrompt("agent "+c.Agents[i].Name, c.Agents[i].Prompt, c.Agents[i].PromptFile)
		if err != nil {
			return err
		}
		c.Agents[i].Prompt = content
	}
	return nil
}

// resolvePrompt returns the resolved prompt text. Exactly one of prompt or
// promptFile must be set.
func (c *Config) resolvePrompt(ownerLabel, prompt, promptFile string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	promptFile = strings.TrimSpace(promptFile)
	switch {
	case prompt != "" && promptFile != "":
		return "", fmt.Errorf("%s: set either prompt or prompt_file, not both", ownerLabel)
	case prompt != "":
		return prompt, nil
	case promptFile != "":
		path := promptFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.configDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: read prompt_file %s: %w", ownerLabel, path, err)
		}
		return strings.TrimSpace(string(data)), nil
	default:
		return "", fmt.Errorf("%s: must set either prompt or prompt_file", ownerLabel)
	}
}

func (c *Config) validate() error {
	if c.Daemon.HTTP.WebhookSecret == "" {
		return errors.New("config: http webhook secret is required (set webhook_secret_env)")
	}
	if c.Daemon.HTTP.DeliveryTTLSeconds < 0 {
		return fmt.Errorf("config: http delivery_ttl_seconds must be positive, got %d", c.Daemon.HTTP.DeliveryTTLSeconds)
	}
	if err := c.validateLogConfig(); err != nil {
		return err
	}
	if err := c.validateBackends(); err != nil {
		return err
	}
	if err := c.validateSkills(); err != nil {
		return err
	}
	if err := c.validateAgents(); err != nil {
		return err
	}
	if err := c.validateProxy(); err != nil {
		return err
	}
	if err := c.validateDispatchConfig(); err != nil {
		return err
	}
	return c.validateRepos()
}

func (c *Config) validateProxy() error {
	p := c.Daemon.Proxy
	if !p.Enabled {
		return nil
	}
	if p.Upstream.URL == "" {
		return errors.New("config: proxy.upstream.url is required when proxy.enabled is true")
	}
	if p.Upstream.Model == "" {
		return errors.New("config: proxy.upstream.model is required when proxy.enabled is true")
	}
	if !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("config: proxy.path must start with '/', got %q", p.Path)
	}
	if p.Upstream.TimeoutSeconds <= 0 {
		return fmt.Errorf("config: proxy.upstream.timeout_seconds must be positive, got %d", p.Upstream.TimeoutSeconds)
	}
	// When an api_key_env is configured, the variable must resolve at startup so
	// that a missing or mis-spelled env var fails fast rather than producing
	// silent 401/403 errors against a protected upstream at request time.
	if p.Upstream.APIKeyEnv != "" && p.Upstream.APIKey == "" {
		return fmt.Errorf("config: proxy.upstream.api_key_env %q is set but the environment variable is empty or unset", p.Upstream.APIKeyEnv)
	}
	return nil
}

func (c *Config) validateDispatchConfig() error {
	d := c.Daemon.Processor.Dispatch
	if d.MaxDepth <= 0 {
		return fmt.Errorf("config: dispatch max_depth must be positive, got %d", d.MaxDepth)
	}
	if d.MaxFanout <= 0 {
		return fmt.Errorf("config: dispatch max_fanout must be positive, got %d", d.MaxFanout)
	}
	if d.DedupWindowSeconds <= 0 {
		return fmt.Errorf("config: dispatch dedup_window_seconds must be positive, got %d", d.DedupWindowSeconds)
	}
	return nil
}

var validLogLevels = []string{"trace", "debug", "info", "warn", "error", "fatal", "panic", "disabled"}

func (c *Config) validateLogConfig() error {
	if c.Daemon.Log.Level != "" {
		if !slices.Contains(validLogLevels, c.Daemon.Log.Level) {
			return fmt.Errorf("config: invalid log level %q (supported: trace, debug, info, warn, error, fatal, panic, disabled)", c.Daemon.Log.Level)
		}
	}
	switch c.Daemon.Log.Format {
	case "json", "text", "":
		// valid
	default:
		return fmt.Errorf("config: unknown log format %q (supported: json, text)", c.Daemon.Log.Format)
	}
	return nil
}

func (c *Config) validateBackends() error {
	if len(c.Daemon.AIBackends) == 0 {
		return errors.New("config: at least one ai_backends entry is required")
	}
	for name, backend := range c.Daemon.AIBackends {
		if !isValidBackendName(name) {
			return fmt.Errorf("config: unsupported ai backend %q (supported: %s)", name, strings.Join(validAIBackendNames, ", "))
		}
		if backend.Command == "" {
			return fmt.Errorf("config: ai backend %q: command is required", name)
		}
	}
	return nil
}

func (c *Config) validateSkills() error {
	for name, skill := range c.Skills {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: skill name is required")
		}
		if skill.Prompt == "" {
			return fmt.Errorf("config: skill %q: prompt is empty after resolution", name)
		}
	}
	return nil
}

func (c *Config) validateAgents() error {
	if len(c.Agents) == 0 {
		return errors.New("config: at least one agent is required")
	}
	seen := make(map[string]struct{}, len(c.Agents))
	for _, a := range c.Agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = struct{}{}

		if a.Backend != "auto" {
			if _, ok := c.Daemon.AIBackends[a.Backend]; !ok {
				return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
			}
		}
		for _, s := range a.Skills {
			if _, ok := c.Skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
		if a.Prompt == "" {
			return fmt.Errorf("config: agent %q: prompt is empty after resolution", a.Name)
		}
	}
	// Validate can_dispatch references after all agents are seen.
	return c.validateDispatchWiring()
}

// validateDispatchWiring checks cross-agent dispatch references:
//  - can_dispatch entries must reference real agents in this config
//  - can_dispatch must not include the agent itself
//  - agents referenced in any can_dispatch list must have a description
func (c *Config) validateDispatchWiring() error {
	// Build set of all agent names for O(1) lookup.
	agentNames := make(map[string]struct{}, len(c.Agents))
	for _, a := range c.Agents {
		agentNames[a.Name] = struct{}{}
	}
	// Build set of agents that appear in any can_dispatch list — these require
	// a description so the roster is informative.
	dispatchTargets := make(map[string]struct{})
	for _, a := range c.Agents {
		for _, t := range a.CanDispatch {
			dispatchTargets[t] = struct{}{}
		}
	}
	for _, a := range c.Agents {
		for _, t := range a.CanDispatch {
			if _, ok := agentNames[t]; !ok {
				return fmt.Errorf("config: agent %q: can_dispatch references unknown agent %q", a.Name, t)
			}
			if t == a.Name {
				return fmt.Errorf("config: agent %q: can_dispatch must not include itself", a.Name)
			}
		}
	}
	// Verify dispatch targets have descriptions.
	for _, a := range c.Agents {
		if _, isTarget := dispatchTargets[a.Name]; isTarget && a.Description == "" {
			return fmt.Errorf("config: agent %q is in a can_dispatch list but has no description (description is required for dispatch targets)", a.Name)
		}
	}
	return nil
}

func (c *Config) validateRepos() error {
	if len(c.Repos) == 0 {
		return errors.New("config: at least one repo is required")
	}
	enabledCount := 0
	seen := make(map[string]struct{}, len(c.Repos))
	for _, r := range c.Repos {
		if r.Name == "" {
			return errors.New("config: repo name is required")
		}
		key := strings.ToLower(r.Name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("config: duplicate repo %q", r.Name)
		}
		seen[key] = struct{}{}
		if r.Enabled {
			enabledCount++
		}
		for i, b := range r.Use {
			if b.Agent == "" {
				return fmt.Errorf("config: repo %q: binding #%d has no agent", r.Name, i)
			}
			if _, ok := c.AgentByName(b.Agent); !ok {
				return fmt.Errorf("config: repo %q: binding references unknown agent %q", r.Name, b.Agent)
			}
			if !b.IsCron() && !b.IsLabel() && !b.IsEvent() {
				return fmt.Errorf("config: repo %q: binding for agent %q has no trigger (set cron, labels, or events)", r.Name, b.Agent)
			}
			triggerCount := 0
			if b.IsLabel() {
				triggerCount++
			}
			if b.IsEvent() {
				triggerCount++
			}
			if b.IsCron() {
				triggerCount++
			}
			if triggerCount > 1 {
				return fmt.Errorf("config: repo %q: binding for agent %q mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger", r.Name, b.Agent)
			}
			for _, kind := range b.Events {
				if _, ok := validEventKinds[kind]; !ok {
					return fmt.Errorf("config: repo %q: binding for agent %q has unknown event kind %q (supported: %s)",
						r.Name, b.Agent, kind, strings.Join(validEventKindsSorted, ", "))
				}
			}
		}
	}
	if enabledCount == 0 {
		return errors.New("config: at least one repo must be enabled")
	}
	return nil
}

func isValidBackendName(name string) bool {
	return slices.Contains(validAIBackendNames, name)
}

func setDefault(dst *string, def string) {
	if strings.TrimSpace(*dst) == "" {
		*dst = def
	}
}

func setDefaultInt[T int | int64](dst *T, def T) {
	if *dst == 0 {
		*dst = def
	}
}
