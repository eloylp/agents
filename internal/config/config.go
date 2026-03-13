package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultHTTPListenAddr          = ":8080"
	defaultHTTPStatusPath          = "/status"
	defaultHTTPWebhookPath         = "/webhooks/github"
	defaultHTTPReadTimeoutSeconds  = 15
	defaultHTTPWriteTimeoutSeconds = 15
	defaultHTTPIdleTimeoutSeconds  = 60
	defaultHTTPMaxBodyBytes        = 1 << 20
	defaultDeliveryTTLSeconds      = 3600
	defaultIssueQueueBufferSize    = 256
	defaultPRQueueBufferSize       = 256
	defaultHTTPShutdownSeconds     = 15
	defaultAITimeoutSeconds        = 600
	defaultMaxPromptChars          = 12000
	defaultAgentsDir               = "agents"

	defaultIssueRefinementPromptFile = "issue_refinement_prompts/PROMPT.md"
	defaultPRReviewPromptFile        = "pr_review_prompts/base/PROMPT.md"
	defaultAutonomousPromptFile      = "autonomous/base/PROMPT.md"
)

type Config struct {
	Log                LogConfig                  `yaml:"log"`
	HTTP               HTTPConfig                 `yaml:"http"`
	Processor          ProcessorConfig            `yaml:"processor"`
	AIBackends         map[string]AIBackendConfig `yaml:"ai_backends"`
	Repos              []RepoConfig               `yaml:"repos"`
	AgentsDir          string                     `yaml:"agents_dir"`
	Prompts            PromptsConfig              `yaml:"prompts"`
	Agents             []AgentConfig              `yaml:"agents"`
	AllowAutonomousPRs bool                       `yaml:"allow_autonomous_prs"`

	AutonomousAgents []AutonomousRepoConfig `yaml:"autonomous_agents"`
}

type PromptsConfig struct {
	IssueRefinement PromptSourceConfig `yaml:"issue_refinement"`
	PRReview        PromptSourceConfig `yaml:"pr_review"`
	Autonomous      PromptSourceConfig `yaml:"autonomous"`
}

type PromptSourceConfig struct {
	PromptFile string `yaml:"prompt_file"`
	Prompt     string `yaml:"prompt"`
}

type AgentConfig struct {
	Name       string `yaml:"name"`
	PromptFile string `yaml:"prompt_file"`
	Prompt     string `yaml:"prompt"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type HTTPConfig struct {
	ListenAddr             string `yaml:"listen_addr"`
	StatusPath             string `yaml:"status_path"`
	WebhookPath            string `yaml:"webhook_path"`
	ReadTimeoutSeconds     int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `yaml:"max_body_bytes"`
	WebhookSecret          string `yaml:"webhook_secret"`
	WebhookSecretEnv       string `yaml:"webhook_secret_env"`
	DeliveryTTLSeconds     int    `yaml:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `yaml:"shutdown_timeout_seconds"`
}

type ProcessorConfig struct {
	IssueQueueBuffer int `yaml:"issue_queue_buffer"`
	PRQueueBuffer    int `yaml:"pr_queue_buffer"`
}

type RepoConfig struct {
	FullName string `yaml:"full_name"`
	Enabled  bool   `yaml:"enabled"`
}

type AIBackendConfig struct {
	Mode             string   `yaml:"mode"`
	Command          string   `yaml:"command"`
	Args             []string `yaml:"args"`
	TimeoutSeconds   int      `yaml:"timeout_seconds"`
	MaxPromptChars   int      `yaml:"max_prompt_chars"`
	RedactionSaltEnv string   `yaml:"redaction_salt_env"`
}

type AutonomousRepoConfig struct {
	Repo    string                  `yaml:"repo"`
	Enabled bool                    `yaml:"enabled"`
	Agents  []AutonomousAgentConfig `yaml:"agents"`
}

type AutonomousAgentConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Cron        string `yaml:"cron"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	setDefault(&c.AgentsDir, defaultAgentsDir)
	c.applyPromptDefaults()
	c.applyHTTPDefaults()
	c.applyProcessorDefaults()
	c.normalizeAgents()
	c.normalizeBackends()
	c.normalizeRepos()
	c.normalizeAutonomousAgents()
	c.resolveWebhookSecret()
}

func (c *Config) applyPromptDefaults() {
	applyPromptSourceDefault(&c.Prompts.IssueRefinement, defaultIssueRefinementPromptFile)
	applyPromptSourceDefault(&c.Prompts.PRReview, defaultPRReviewPromptFile)
	applyPromptSourceDefault(&c.Prompts.Autonomous, defaultAutonomousPromptFile)
}

func applyPromptSourceDefault(src *PromptSourceConfig, defaultFile string) {
	src.PromptFile = strings.TrimSpace(src.PromptFile)
	src.Prompt = strings.TrimSpace(src.Prompt)
	if src.PromptFile == "" && src.Prompt == "" {
		src.PromptFile = defaultFile
	}
}

func (c *Config) applyHTTPDefaults() {
	setDefault(&c.HTTP.ListenAddr, defaultHTTPListenAddr)
	setDefault(&c.HTTP.StatusPath, defaultHTTPStatusPath)
	setDefault(&c.HTTP.WebhookPath, defaultHTTPWebhookPath)
	setDefaultInt(&c.HTTP.ReadTimeoutSeconds, defaultHTTPReadTimeoutSeconds)
	setDefaultInt(&c.HTTP.WriteTimeoutSeconds, defaultHTTPWriteTimeoutSeconds)
	setDefaultInt(&c.HTTP.IdleTimeoutSeconds, defaultHTTPIdleTimeoutSeconds)
	setDefaultInt64(&c.HTTP.MaxBodyBytes, defaultHTTPMaxBodyBytes)
	setDefaultInt(&c.HTTP.DeliveryTTLSeconds, defaultDeliveryTTLSeconds)
	setDefaultInt(&c.HTTP.ShutdownTimeoutSeconds, defaultHTTPShutdownSeconds)
}

func (c *Config) applyProcessorDefaults() {
	setDefaultInt(&c.Processor.IssueQueueBuffer, defaultIssueQueueBufferSize)
	setDefaultInt(&c.Processor.PRQueueBuffer, defaultPRQueueBufferSize)
}

func (c *Config) normalizeAgents() {
	for i := range c.Agents {
		c.Agents[i].Name = strings.ToLower(strings.TrimSpace(c.Agents[i].Name))
		c.Agents[i].PromptFile = strings.TrimSpace(c.Agents[i].PromptFile)
		c.Agents[i].Prompt = strings.TrimSpace(c.Agents[i].Prompt)
	}
}

func (c *Config) normalizeBackends() {
	normalized := make(map[string]AIBackendConfig, len(c.AIBackends))
	for name, backend := range c.AIBackends {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		setDefault(&backend.Mode, "noop")
		setDefaultInt(&backend.TimeoutSeconds, defaultAITimeoutSeconds)
		setDefaultInt(&backend.MaxPromptChars, defaultMaxPromptChars)
		normalized[key] = backend
	}
	c.AIBackends = normalized
}

func (c *Config) normalizeRepos() {
	for i := range c.Repos {
		c.Repos[i].FullName = strings.TrimSpace(c.Repos[i].FullName)
	}
}

func (c *Config) normalizeAutonomousAgents() {
	for i := range c.AutonomousAgents {
		c.AutonomousAgents[i].Repo = strings.TrimSpace(c.AutonomousAgents[i].Repo)
		for j := range c.AutonomousAgents[i].Agents {
			a := &c.AutonomousAgents[i].Agents[j]
			a.Name = strings.TrimSpace(a.Name)
			a.Cron = strings.TrimSpace(a.Cron)
			a.Description = strings.TrimSpace(a.Description)
		}
	}
}

func (c *Config) resolveWebhookSecret() {
	if c.HTTP.WebhookSecret == "" && c.HTTP.WebhookSecretEnv != "" {
		c.HTTP.WebhookSecret = os.Getenv(c.HTTP.WebhookSecretEnv)
	}
}

func (c *Config) validate() error {
	if c.HTTP.WebhookSecret == "" {
		return errors.New("config: http webhook secret is required")
	}
	if err := c.validateBackends(); err != nil {
		return err
	}
	if err := c.validateRepos(); err != nil {
		return err
	}
	if err := c.validatePromptSources(); err != nil {
		return err
	}
	agentNames, err := c.validateAgents()
	if err != nil {
		return err
	}
	return c.validateAutonomousAgents(agentNames)
}

func (c *Config) validateBackends() error {
	if len(c.AIBackends) == 0 {
		return errors.New("config: at least one ai_backends entry is required")
	}
	for name := range c.AIBackends {
		if name != "claude" && name != "codex" {
			return fmt.Errorf("config: unsupported ai backend %q (supported: claude, codex)", name)
		}
	}
	return nil
}

func (c *Config) validateRepos() error {
	if len(c.Repos) == 0 {
		return errors.New("config: at least one repo is required")
	}
	for _, repo := range c.Repos {
		if repo.FullName == "" {
			return errors.New("config: repo full_name is required")
		}
	}
	return nil
}

func (c *Config) validatePromptSources() error {
	sources := map[string]PromptSourceConfig{
		"prompts.issue_refinement": c.Prompts.IssueRefinement,
		"prompts.pr_review":        c.Prompts.PRReview,
		"prompts.autonomous":       c.Prompts.Autonomous,
	}
	for name, src := range sources {
		if src.PromptFile != "" && src.Prompt != "" {
			return fmt.Errorf("config: %s must have only one of prompt_file or prompt, not both", name)
		}
	}
	return nil
}

func (c *Config) validateAgents() (map[string]struct{}, error) {
	names := make(map[string]struct{}, len(c.Agents))
	for _, agent := range c.Agents {
		if agent.Name == "" {
			return nil, errors.New("config: agent name is required")
		}
		if _, dup := names[agent.Name]; dup {
			return nil, fmt.Errorf("config: duplicate agent name %q", agent.Name)
		}
		hasFile := agent.PromptFile != ""
		hasInline := agent.Prompt != ""
		if !hasFile && !hasInline {
			return nil, fmt.Errorf("config: agent %q must have either prompt_file or prompt", agent.Name)
		}
		if hasFile && hasInline {
			return nil, fmt.Errorf("config: agent %q must have only one of prompt_file or prompt, not both", agent.Name)
		}
		names[agent.Name] = struct{}{}
	}
	return names, nil
}

func (c *Config) validateAutonomousAgents(agentNames map[string]struct{}) error {
	for _, repo := range c.AutonomousAgents {
		if repo.Repo == "" {
			return errors.New("config: autonomous agent repo is required")
		}
		for _, agent := range repo.Agents {
			if agent.Name != strings.ToLower(strings.TrimSpace(agent.Name)) {
				return fmt.Errorf("config: autonomous agent name must be lowercase and trimmed for repo %s", repo.Repo)
			}
			if agent.Name == "" {
				return fmt.Errorf("config: autonomous agent name required for repo %s", repo.Repo)
			}
			if agent.Cron == "" {
				return fmt.Errorf("config: autonomous agent cron required for repo %s", repo.Repo)
			}
			if _, ok := agentNames[agent.Name]; !ok {
				return fmt.Errorf("config: autonomous agent %q for repo %s references unknown agent", agent.Name, repo.Repo)
			}
		}
	}
	return nil
}

// AgentByName returns the agent configuration for the given name.
func (c *Config) AgentByName(name string) (AgentConfig, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, agent := range c.Agents {
		if agent.Name == normalized {
			return agent, true
		}
	}
	return AgentConfig{}, false
}

// AgentNames returns all defined agent names.
func (c *Config) AgentNames() []string {
	names := make([]string, len(c.Agents))
	for i, agent := range c.Agents {
		names[i] = agent.Name
	}
	return names
}

func (c *Config) RepoByName(fullName string) (RepoConfig, bool) {
	for _, repo := range c.Repos {
		if strings.EqualFold(repo.FullName, fullName) {
			return repo, true
		}
	}
	return RepoConfig{}, false
}

// DefaultConfiguredBackend returns the name of the preferred backend when no
// explicit backend is specified in a label. claude is preferred over codex
// when both are configured.
func (c *Config) DefaultConfiguredBackend() string {
	if _, ok := c.AIBackends["claude"]; ok {
		return "claude"
	}
	if _, ok := c.AIBackends["codex"]; ok {
		return "codex"
	}
	return ""
}

func setDefault(field *string, value string) {
	if strings.TrimSpace(*field) == "" {
		*field = value
	}
}

func setDefaultInt(field *int, value int) {
	if *field == 0 {
		*field = value
	}
}

func setDefaultInt64(field *int64, value int64) {
	if *field == 0 {
		*field = value
	}
}
