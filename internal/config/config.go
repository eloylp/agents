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
	defaultHTTPAgentsRunPath       = "/agents/run"
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
	defaultMemoryDir               = "/var/lib/agents/memory"

	defaultIssueRefinementPromptFile = "issue_refinement_prompts/PROMPT.md"
	defaultPRReviewPromptFile        = "pr_review_prompts/base/PROMPT.md"
	defaultAutonomousPromptFile      = "autonomous/base/PROMPT.md"

)

type Config struct {
	Log        LogConfig                  `yaml:"log"`
	HTTP       HTTPConfig                 `yaml:"http"`
	Processor  ProcessorConfig            `yaml:"processor"`
	AIBackends map[string]AIBackendConfig `yaml:"ai_backends"`
	Repos      []RepoConfig               `yaml:"repos"`
	AgentsDir  string                     `yaml:"agents_dir"`
	MemoryDir  string                     `yaml:"memory_dir"`
	Prompts    PromptsConfig              `yaml:"prompts"`
	Skills     []SkillConfig              `yaml:"skills"`
	Agents     []AgentConfig              `yaml:"agents"`

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


type SkillConfig struct {
	Name       string `yaml:"name"`
	PromptFile string `yaml:"prompt_file"`
	Prompt     string `yaml:"prompt"`
}

type AgentConfig struct {
	Name   string   `yaml:"name"`
	Skills []string `yaml:"skills"`
}

type TaskConfig struct {
	Name   string `yaml:"name"`
	Prompt string `yaml:"prompt"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type HTTPConfig struct {
	ListenAddr             string `yaml:"listen_addr"`
	StatusPath             string `yaml:"status_path"`
	WebhookPath            string `yaml:"webhook_path"`
	AgentsRunPath          string `yaml:"agents_run_path"`
	APIKeyEnv              string `yaml:"api_key_env"`
	APIKey                 string `yaml:"-"`
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
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Cron        string       `yaml:"cron"`
	Backend     string       `yaml:"backend"`
	Enabled     *bool        `yaml:"enabled"`
	Skills      []string     `yaml:"skills"`
	Tasks       []TaskConfig `yaml:"tasks"`
}

// IsEnabled reports whether this agent should be scheduled. When the field is
// omitted from config, agents are enabled by default.
func (a AutonomousAgentConfig) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
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
	setDefault(&c.MemoryDir, defaultMemoryDir)
	c.applyPromptDefaults()
	c.applyHTTPDefaults()
	c.applyProcessorDefaults()
	c.normalizeSkills()
	c.normalizeAgents()
	c.normalizeBackends()
	c.normalizeRepos()
	c.normalizeAutonomousAgents()
	c.resolveWebhookSecret()
	c.resolveAPIKey()
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
	setDefault(&c.HTTP.AgentsRunPath, defaultHTTPAgentsRunPath)
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

func (c *Config) normalizeSkills() {
	for i := range c.Skills {
		c.Skills[i].Name = strings.ToLower(strings.TrimSpace(c.Skills[i].Name))
		c.Skills[i].PromptFile = strings.TrimSpace(c.Skills[i].PromptFile)
		c.Skills[i].Prompt = strings.TrimSpace(c.Skills[i].Prompt)
	}
}

func (c *Config) normalizeAgents() {
	for i := range c.Agents {
		c.Agents[i].Name = strings.ToLower(strings.TrimSpace(c.Agents[i].Name))
		for j := range c.Agents[i].Skills {
			c.Agents[i].Skills[j] = strings.ToLower(strings.TrimSpace(c.Agents[i].Skills[j]))
		}
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
		backend.Command = strings.TrimSpace(backend.Command)
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
			a.Backend = strings.ToLower(strings.TrimSpace(a.Backend))
			if a.Backend == "" {
				a.Backend = "auto"
			}
			for k := range a.Skills {
				a.Skills[k] = strings.ToLower(strings.TrimSpace(a.Skills[k]))
			}
			for k := range a.Tasks {
				a.Tasks[k].Name = strings.ToLower(strings.TrimSpace(a.Tasks[k].Name))
				a.Tasks[k].Prompt = strings.TrimSpace(a.Tasks[k].Prompt)
			}
		}
	}
}

func (c *Config) resolveWebhookSecret() {
	if c.HTTP.WebhookSecret == "" && c.HTTP.WebhookSecretEnv != "" {
		c.HTTP.WebhookSecret = os.Getenv(c.HTTP.WebhookSecretEnv)
	}
}

func (c *Config) resolveAPIKey() {
	if c.HTTP.APIKey == "" && c.HTTP.APIKeyEnv != "" {
		c.HTTP.APIKey = os.Getenv(c.HTTP.APIKeyEnv)
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
	skillNames, err := c.validateSkills()
	if err != nil {
		return err
	}
	if err := c.validateAgents(skillNames); err != nil {
		return err
	}
	return c.validateAutonomousAgents(skillNames)
}

func (c *Config) validateBackends() error {
	if len(c.AIBackends) == 0 {
		return errors.New("config: at least one ai_backends entry is required")
	}
	for name, backend := range c.AIBackends {
		if name != "claude" && name != "codex" {
			return fmt.Errorf("config: unsupported ai backend %q (supported: claude, codex)", name)
		}
		if backend.Mode != "noop" && backend.Mode != "command" {
			return fmt.Errorf("config: backend %q has unsupported mode %q (supported: noop, command)", name, backend.Mode)
		}
		if backend.Mode == "command" && strings.TrimSpace(backend.Command) == "" {
			return fmt.Errorf("config: ai backend %q has mode=command but no command specified", name)
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

func (c *Config) validateSkills() (map[string]struct{}, error) {
	names := make(map[string]struct{}, len(c.Skills))
	for _, skill := range c.Skills {
		if skill.Name == "" {
			return nil, errors.New("config: skill name is required")
		}
		if _, dup := names[skill.Name]; dup {
			return nil, fmt.Errorf("config: duplicate skill name %q", skill.Name)
		}
		hasFile := skill.PromptFile != ""
		hasInline := skill.Prompt != ""
		if !hasFile && !hasInline {
			return nil, fmt.Errorf("config: skill %q must have either prompt_file or prompt", skill.Name)
		}
		if hasFile && hasInline {
			return nil, fmt.Errorf("config: skill %q must have only one of prompt_file or prompt, not both", skill.Name)
		}
		names[skill.Name] = struct{}{}
	}
	return names, nil
}

func (c *Config) validateAgents(skillNames map[string]struct{}) error {
	names := make(map[string]struct{}, len(c.Agents))
	for _, agent := range c.Agents {
		if agent.Name == "" {
			return errors.New("config: agent name is required")
		}
		if _, dup := names[agent.Name]; dup {
			return fmt.Errorf("config: duplicate agent name %q", agent.Name)
		}
		if len(agent.Skills) == 0 {
			return fmt.Errorf("config: agent %q must reference at least one skill", agent.Name)
		}
		for _, skill := range agent.Skills {
			if _, ok := skillNames[skill]; !ok {
				return fmt.Errorf("config: agent %q references unknown skill %q", agent.Name, skill)
			}
		}
		names[agent.Name] = struct{}{}
	}
	return nil
}

func (c *Config) validateAutonomousAgents(skillNames map[string]struct{}) error {
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
			if agent.Backend != "auto" && agent.Backend != "claude" && agent.Backend != "codex" {
				return fmt.Errorf("config: autonomous agent backend %q for repo %s must be one of auto, claude, codex", agent.Backend, repo.Repo)
			}
			if len(agent.Skills) == 0 {
				return fmt.Errorf("config: autonomous agent %q for repo %s must reference at least one skill", agent.Name, repo.Repo)
			}
			for _, skill := range agent.Skills {
				if _, ok := skillNames[skill]; !ok {
					return fmt.Errorf("config: autonomous agent %q for repo %s references unknown skill %q", agent.Name, repo.Repo, skill)
				}
			}
			if len(agent.Tasks) == 0 {
				return fmt.Errorf("config: autonomous agent %q for repo %s must define at least one task", agent.Name, repo.Repo)
			}
			for _, task := range agent.Tasks {
				if task.Name == "" {
					return fmt.Errorf("config: autonomous agent %q for repo %s has a task with empty name", agent.Name, repo.Repo)
				}
				if task.Prompt == "" {
					return fmt.Errorf("config: autonomous agent %q for repo %s task %q must have a prompt", agent.Name, repo.Repo, task.Name)
				}
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
