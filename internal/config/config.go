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
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/fleet"
)

// validAIBackendNames is the canonical list of supported AI backend names.
// Adding a new backend only requires updating this slice.
var validAIBackendNames = []string{"claude", "codex", "claude_local"}

// validEventKinds is the set of event kind strings accepted in the events:
// binding field. It must be kept in sync with the kinds emitted by the webhook
// server handlers.
var validEventKinds = map[string]struct{}{
	"issues.labeled":                      {},
	"issues.opened":                       {},
	"issues.edited":                       {},
	"issues.reopened":                     {},
	"issues.closed":                       {},
	"pull_request.labeled":                {},
	"pull_request.opened":                 {},
	"pull_request.synchronize":            {},
	"pull_request.ready_for_review":       {},
	"pull_request.closed":                 {},
	"issue_comment.created":               {},
	"pull_request_review.submitted":       {},
	"pull_request_review_comment.created": {},
	"push":                                {},
}

// validEventKindsSorted is a precomputed, sorted list of validEventKinds keys
// for use in human-readable error messages.
var validEventKindsSorted = slices.Sorted(maps.Keys(validEventKinds))

const (
	defaultHTTPListenAddr          = ":8080"
	defaultHTTPStatusPath          = "/status"
	defaultHTTPWebhookPath         = "/webhooks/github"
	defaultHTTPReadTimeoutSeconds  = 15
	defaultHTTPWriteTimeoutSeconds = 15
	defaultHTTPIdleTimeoutSeconds  = 60
	defaultHTTPMaxBodyBytes        = 1 << 20
	defaultDeliveryTTLSeconds      = 3600
	defaultHTTPShutdownSeconds     = 15

	defaultEventQueueBufferSize = 256
	defaultMaxConcurrentAgents  = 4

	defaultProxyPath           = "/v1/messages"
	defaultProxyTimeoutSeconds = 120
)

// Config is the root configuration loaded from YAML.
type Config struct {
	Daemon DaemonConfig        `yaml:"daemon"`
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
	Log        LogConfig                  `yaml:"log"`
	HTTP       HTTPConfig                 `yaml:"http"`
	Processor  ProcessorConfig            `yaml:"processor"`
	AIBackends map[string]fleet.Backend `yaml:"ai_backends"`
	Proxy      ProxyConfig                `yaml:"proxy"`
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

// ValidateCrossRefs checks cross-entity reference consistency across the four
// mutable entity sets. It is called by the SQLite CRUD layer after each write
// (within the same transaction) so that invalid fleet configurations cannot be
// committed to the database.
//
// Specifically it verifies:
//   - every agent references a known backend and known skills
//   - dispatch wiring (can_dispatch) references existing agents with descriptions
//   - every repo binding references a known agent
func ValidateCrossRefs(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	agentByName := make(map[string]fleet.Agent, len(agents))
	for _, a := range agents {
		agentByName[a.Name] = a
	}

	// Validate agent → backend and skill references.
	for _, a := range agents {
		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := backends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		if err := validateAgentModel(a.Name, a.Model, backends[a.Backend]); err != nil {
			return err
		}
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
	}

	// Validate can_dispatch wiring.
	for _, a := range agents {
		for _, t := range a.CanDispatch {
			target, ok := agentByName[t]
			if !ok {
				return fmt.Errorf("config: agent %q: can_dispatch references unknown agent %q", a.Name, t)
			}
			if t == a.Name {
				return fmt.Errorf("config: agent %q: can_dispatch must not include itself", a.Name)
			}
			if target.Description == "" {
				return fmt.Errorf("config: agent %q is in a can_dispatch list but has no description (description is required for dispatch targets)", t)
			}
		}
	}

	// Validate repo binding → agent references.
	for _, r := range repos {
		for i, b := range r.Use {
			if _, ok := agentByName[b.Agent]; !ok {
				return fmt.Errorf("config: repo %q: binding #%d references unknown agent %q", r.Name, i, b.Agent)
			}
		}
	}

	return nil
}

// ValidateEntities runs entity-level (non-daemon) invariants on the four
// mutable entity sets. It is a superset of ValidateCrossRefs: it additionally
// checks field-level constraints (non-empty prompts, valid backend names,
// binding trigger types) but does NOT enforce aggregate minimums ("at least one
// agent/repo/backend required") — those are enforced separately on DELETE paths
// so that incremental UPSERT builds remain possible.
//
// The intent is that every CRUD write on the SQLite store passes ValidateEntities
// so that SQLite is never left in a state that would fail LoadAndValidate on
// restart due to locally invalid entity fields.
func ValidateEntities(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	if backends == nil {
		backends = map[string]fleet.Backend{}
	}
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}

	// Backend field checks (without "at least one" aggregate check).
	for name, b := range backends {
		if !isSupportedBackend(name, b) {
			return fmt.Errorf("config: unsupported ai backend %q (supported: %s, or any custom name with local_model_url set)", name, strings.Join(validAIBackendNames, ", "))
		}
		if b.Command == "" {
			return fmt.Errorf("config: ai backend %q: command is required", name)
		}
	}

	// Skill field checks.
	for name, s := range skills {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: skill name is required")
		}
		if s.Prompt == "" {
			return fmt.Errorf("config: skill %q: prompt is empty after resolution", name)
		}
	}

	// Agent field checks, backend/skill cross-refs, and dispatch wiring
	// (without "at least one" aggregate check).
	seen := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = struct{}{}
		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := backends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		if err := validateAgentModel(a.Name, a.Model, backends[a.Backend]); err != nil {
			return err
		}
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
		if a.Prompt == "" {
			return fmt.Errorf("config: agent %q: prompt is empty after resolution", a.Name)
		}
	}
	// Dispatch wiring reuses the Config method which only reads c.Agents.
	if err := (&Config{Agents: agents}).validateDispatchWiring(); err != nil {
		return err
	}

	// Repo binding field checks and agent cross-refs (without "at least one"
	// aggregate check). Agent lookup uses the set built above.
	agentSet := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		agentSet[strings.ToLower(a.Name)] = struct{}{}
	}
	seenRepos := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		if r.Name == "" {
			return errors.New("config: repo name is required")
		}
		key := strings.ToLower(r.Name)
		if _, dup := seenRepos[key]; dup {
			return fmt.Errorf("config: duplicate repo %q", r.Name)
		}
		seenRepos[key] = struct{}{}
		for i, b := range r.Use {
			if b.Agent == "" {
				return fmt.Errorf("config: repo %q: binding #%d has no agent", r.Name, i)
			}
			if _, ok := agentSet[strings.ToLower(b.Agent)]; !ok {
				return fmt.Errorf("config: repo %q: binding references unknown agent %q", r.Name, b.Agent)
			}
			if !b.IsCron() && !b.IsLabel() && !b.IsEvent() {
				return fmt.Errorf("config: repo %q: binding for agent %q has no trigger (set cron, labels, or events)", r.Name, b.Agent)
			}
			if countBindingTriggers(b) > 1 {
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
	return nil
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

// ─── internal: defaults, normalization, secrets, prompt loading, validation ──

func (c *Config) applyDefaults() {
	// daemon.http
	setDefault(&c.Daemon.HTTP.ListenAddr, defaultHTTPListenAddr)
	setDefault(&c.Daemon.HTTP.StatusPath, defaultHTTPStatusPath)
	setDefault(&c.Daemon.HTTP.WebhookPath, defaultHTTPWebhookPath)
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
		fleet.ApplyBackendDefaults(&backend)
		c.Daemon.AIBackends[name] = backend
	}

	// repos: default enabled to true when field absent is ambiguous; YAML
	// zero-value is false. We leave it as-is — absent means false here,
	// because repos are an explicit allow-list.
}

func (c *Config) normalize() {
	// Lowercase backend keys for case-insensitive matching.
	if len(c.Daemon.AIBackends) > 0 {
		lower := make(map[string]fleet.Backend, len(c.Daemon.AIBackends))
		for name, backend := range c.Daemon.AIBackends {
			key := fleet.NormalizeBackendName(name)
			fleet.NormalizeBackend(&backend)
			lower[key] = backend
		}
		c.Daemon.AIBackends = lower
	}

	// Lowercase skill keys.
	if len(c.Skills) > 0 {
		lower := make(map[string]fleet.Skill, len(c.Skills))
		for name, skill := range c.Skills {
			key := fleet.NormalizeSkillName(name)
			fleet.NormalizeSkill(&skill)
			lower[key] = skill
		}
		c.Skills = lower
	}

	// Agents.
	for i := range c.Agents {
		fleet.NormalizeAgent(&c.Agents[i])
	}

	// Repos.
	for i := range c.Repos {
		fleet.NormalizeRepo(&c.Repos[i])
	}

	// Log.
	c.Daemon.Log.Level = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Level))
	c.Daemon.Log.Format = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Format))
}

func (c *Config) resolveSecrets() {
	if c.Daemon.HTTP.WebhookSecret == "" && c.Daemon.HTTP.WebhookSecretEnv != "" {
		c.Daemon.HTTP.WebhookSecret = os.Getenv(c.Daemon.HTTP.WebhookSecretEnv)
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
var validLogFormats = []string{"json", "text"}

func (c *Config) validateLogConfig() error {
	if c.Daemon.Log.Level != "" {
		if !slices.Contains(validLogLevels, c.Daemon.Log.Level) {
			return fmt.Errorf("config: invalid log level %q (supported: %s)", c.Daemon.Log.Level, strings.Join(validLogLevels, ", "))
		}
	}
	if c.Daemon.Log.Format != "" && !slices.Contains(validLogFormats, c.Daemon.Log.Format) {
		return fmt.Errorf("config: unknown log format %q (supported: %s)", c.Daemon.Log.Format, strings.Join(validLogFormats, ", "))
	}
	return nil
}

func (c *Config) validateBackends() error {
	for name, backend := range c.Daemon.AIBackends {
		if !isSupportedBackend(name, backend) {
			return fmt.Errorf("config: unsupported ai backend %q (supported: %s, or any custom name with local_model_url set)", name, strings.Join(validAIBackendNames, ", "))
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
	seen := make(map[string]struct{}, len(c.Agents))
	for _, a := range c.Agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = struct{}{}

		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := c.Daemon.AIBackends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		if err := validateAgentModel(a.Name, a.Model, c.Daemon.AIBackends[a.Backend]); err != nil {
			return err
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
//   - can_dispatch entries must reference real agents in this config
//   - can_dispatch must not include the agent itself
//   - agents referenced in any can_dispatch list must have a description
func (c *Config) validateDispatchWiring() error {
	agentByName := make(map[string]fleet.Agent, len(c.Agents))
	for _, a := range c.Agents {
		agentByName[a.Name] = a
	}
	for _, a := range c.Agents {
		for _, t := range a.CanDispatch {
			target, ok := agentByName[t]
			if !ok {
				return fmt.Errorf("config: agent %q: can_dispatch references unknown agent %q", a.Name, t)
			}
			if t == a.Name {
				return fmt.Errorf("config: agent %q: can_dispatch must not include itself", a.Name)
			}
			if target.Description == "" {
				return fmt.Errorf("config: agent %q is in a can_dispatch list but has no description (description is required for dispatch targets)", t)
			}
		}
	}
	return nil
}

// countBindingTriggers returns the number of trigger types (labels, events, cron) set on b.
func countBindingTriggers(b fleet.Binding) int {
	n := 0
	if b.IsLabel() {
		n++
	}
	if b.IsEvent() {
		n++
	}
	if b.IsCron() {
		n++
	}
	return n
}

// validateRepos checks per-repo invariants: name presence, name uniqueness,
// binding agent references, trigger exclusivity, and event-kind allow-list.
// It does NOT enforce an "at least one enabled repo" minimum — disabling all
// repos is a legitimate user action (fleet maintenance, evaluating prompts on
// a different repo) and the daemon runs cleanly with zero enabled repos:
// webhook events for disabled repos route through workflow_engine which logs
// "no bindings matched event, skipping". See issue #302.
func (c *Config) validateRepos() error {
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
			if countBindingTriggers(b) > 1 {
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
	return nil
}

func isValidBackendName(name string) bool {
	return slices.Contains(validAIBackendNames, name)
}

func isSupportedBackend(name string, backend fleet.Backend) bool {
	if isValidBackendName(name) {
		return true
	}
	return strings.TrimSpace(backend.LocalModelURL) != ""
}

func validateAgentModel(_ string, _ string, _ fleet.Backend) error {
	// Model/backend mismatches are intentionally allowed at config validation
	// time so discovery can persist backend model changes even if agents become
	// temporarily orphaned. Runtime paths enforce this strictly before invoking
	// a backend, and UI surfaces orphan remediation flows.
	return nil
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
