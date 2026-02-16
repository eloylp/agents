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
	defaultAITimeoutSeconds        = 600
	defaultMaxPromptChars          = 12000
)

var defaultAgents = []string{"architect", "security", "testing", "devops", "ux"}

type Config struct {
	Log        LogConfig                  `yaml:"log"`
	HTTP       HTTPConfig                 `yaml:"http"`
	AIBackends map[string]AIBackendConfig `yaml:"ai_backends"`
	Repos      []RepoConfig               `yaml:"repos"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type HTTPConfig struct {
	ListenAddr          string `yaml:"listen_addr"`
	StatusPath          string `yaml:"status_path"`
	WebhookPath         string `yaml:"webhook_path"`
	ReadTimeoutSeconds  int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds  int    `yaml:"idle_timeout_seconds"`
	MaxBodyBytes        int64  `yaml:"max_body_bytes"`
	WebhookSecret       string `yaml:"webhook_secret"`
	WebhookSecretEnv    string `yaml:"webhook_secret_env"`
	DeliveryTTLSeconds  int    `yaml:"delivery_ttl_seconds"`
	IssueQueueBuffer    int    `yaml:"issue_queue_buffer"`
	PRQueueBuffer       int    `yaml:"pr_queue_buffer"`
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
	Agents           []string `yaml:"agents"`
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
	if err := cfg.resolveEnv(); err != nil {
		return nil, err
	}
	if len(cfg.Repos) == 0 {
		return nil, errors.New("config: at least one repo is required")
	}
	for _, repo := range cfg.Repos {
		if strings.TrimSpace(repo.FullName) == "" {
			return nil, errors.New("config: repo full_name is required")
		}
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.HTTP.ListenAddr) == "" {
		c.HTTP.ListenAddr = defaultHTTPListenAddr
	}
	if strings.TrimSpace(c.HTTP.StatusPath) == "" {
		c.HTTP.StatusPath = defaultHTTPStatusPath
	}
	if strings.TrimSpace(c.HTTP.WebhookPath) == "" {
		c.HTTP.WebhookPath = defaultHTTPWebhookPath
	}
	if c.HTTP.ReadTimeoutSeconds == 0 {
		c.HTTP.ReadTimeoutSeconds = defaultHTTPReadTimeoutSeconds
	}
	if c.HTTP.WriteTimeoutSeconds == 0 {
		c.HTTP.WriteTimeoutSeconds = defaultHTTPWriteTimeoutSeconds
	}
	if c.HTTP.IdleTimeoutSeconds == 0 {
		c.HTTP.IdleTimeoutSeconds = defaultHTTPIdleTimeoutSeconds
	}
	if c.HTTP.MaxBodyBytes == 0 {
		c.HTTP.MaxBodyBytes = defaultHTTPMaxBodyBytes
	}
	if c.HTTP.DeliveryTTLSeconds == 0 {
		c.HTTP.DeliveryTTLSeconds = defaultDeliveryTTLSeconds
	}
	if c.HTTP.IssueQueueBuffer == 0 {
		c.HTTP.IssueQueueBuffer = defaultIssueQueueBufferSize
	}
	if c.HTTP.PRQueueBuffer == 0 {
		c.HTTP.PRQueueBuffer = defaultPRQueueBufferSize
	}
	normalizedBackends := make(map[string]AIBackendConfig, len(c.AIBackends))
	for name, backend := range c.AIBackends {
		normalizedName := strings.ToLower(strings.TrimSpace(name))
		if normalizedName == "" {
			continue
		}
		if backend.Mode == "" {
			backend.Mode = "noop"
		}
		if backend.TimeoutSeconds == 0 {
			backend.TimeoutSeconds = defaultAITimeoutSeconds
		}
		if backend.MaxPromptChars == 0 {
			backend.MaxPromptChars = defaultMaxPromptChars
		}
		if len(backend.Agents) == 0 {
			backend.Agents = append([]string(nil), defaultAgents...)
		}
		for i := range backend.Agents {
			backend.Agents[i] = strings.ToLower(strings.TrimSpace(backend.Agents[i]))
		}
		normalizedBackends[normalizedName] = backend
	}
	c.AIBackends = normalizedBackends
	for i := range c.Repos {
		c.Repos[i].FullName = strings.TrimSpace(c.Repos[i].FullName)
	}
}

func (c *Config) resolveEnv() error {
	if c.HTTP.WebhookSecret == "" && c.HTTP.WebhookSecretEnv != "" {
		c.HTTP.WebhookSecret = os.Getenv(c.HTTP.WebhookSecretEnv)
	}
	if c.HTTP.WebhookSecret == "" {
		return errors.New("config: http webhook secret is required")
	}
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

func (c *Config) RepoByName(fullName string) (RepoConfig, bool) {
	for _, repo := range c.Repos {
		if strings.EqualFold(repo.FullName, fullName) {
			return repo, true
		}
	}
	return RepoConfig{}, false
}

func (c *Config) DefaultConfiguredBackend() string {
	if _, ok := c.AIBackends["claude"]; ok {
		return "claude"
	}
	if _, ok := c.AIBackends["codex"]; ok {
		return "codex"
	}
	return ""
}
