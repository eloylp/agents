package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultPollIntervalSeconds     = 60
	defaultPerPage                 = 50
	defaultMaxItemsPerPoll         = 200
	defaultMaxIdleIntervalSeconds  = 600
	defaultJitterSeconds           = 5
	defaultCommentFingerprintLimit = 5
	defaultFileFingerprintLimit    = 50
	defaultMaxFingerprintBytes     = 20000
	defaultMaxPostsPerRun          = 10
	defaultMaxRunsPerHour          = 5
	defaultMaxRunsPerDay           = 20
	defaultAITimeoutSeconds        = 600
	defaultMaxPromptChars          = 12000
)

var defaultRoles = []string{"architect", "security", "testing", "devops", "ux"}

type Config struct {
	Log        LogConfig                  `yaml:"log"`
	Database   DatabaseConfig             `yaml:"database"`
	GitHub     GitHubConfig               `yaml:"github"`
	Poller     PollerConfig               `yaml:"poller"`
	AIBackends map[string]AIBackendConfig `yaml:"ai_backends"`
	Repos      []RepoConfig               `yaml:"repos"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type DatabaseConfig struct {
	DSN         string `yaml:"dsn"`
	DSNEnv      string `yaml:"dsn_env"`
	AutoMigrate bool   `yaml:"auto_migrate"`
}

type GitHubConfig struct {
	Token      string `yaml:"token"`
	TokenEnv   string `yaml:"token_env"`
	APIBaseURL string `yaml:"api_base_url"`
}

type PollerConfig struct {
	PerPage                 int `yaml:"per_page"`
	MaxItemsPerPoll         int `yaml:"max_items_per_poll"`
	MaxIdleIntervalSeconds  int `yaml:"max_idle_interval_seconds"`
	JitterSeconds           int `yaml:"jitter_seconds"`
	CommentFingerprintLimit int `yaml:"comment_fingerprint_limit"`
	FileFingerprintLimit    int `yaml:"file_fingerprint_limit"`
	MaxFingerprintBytes     int `yaml:"max_fingerprint_bytes"`
	MaxPostsPerRun          int `yaml:"max_posts_per_run"`
	MaxRunsPerHour          int `yaml:"max_runs_per_hour"`
	MaxRunsPerDay           int `yaml:"max_runs_per_day"`
}

type ClaudeConfig struct {
	Mode             string   `yaml:"mode"`
	Command          string   `yaml:"command"`
	Args             []string `yaml:"args"`
	TimeoutSeconds   int      `yaml:"timeout_seconds"`
	MaxPromptChars   int      `yaml:"max_prompt_chars"`
	RedactionSaltEnv string   `yaml:"redaction_salt_env"`
}

type OpenAIConfig struct {
	Mode             string   `yaml:"mode"`
	Command          string   `yaml:"command"`
	Args             []string `yaml:"args"`
	TimeoutSeconds   int      `yaml:"timeout_seconds"`
	MaxPromptChars   int      `yaml:"max_prompt_chars"`
	RedactionSaltEnv string   `yaml:"redaction_salt_env"`
}

type RepoConfig struct {
	FullName            string `yaml:"full_name"`
	Enabled             bool   `yaml:"enabled"`
	PollIntervalSeconds int    `yaml:"poll_interval_seconds"`
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
	if c.Poller.PerPage == 0 {
		c.Poller.PerPage = defaultPerPage
	}
	if c.Poller.MaxItemsPerPoll == 0 {
		c.Poller.MaxItemsPerPoll = defaultMaxItemsPerPoll
	}
	if c.Poller.MaxIdleIntervalSeconds == 0 {
		c.Poller.MaxIdleIntervalSeconds = defaultMaxIdleIntervalSeconds
	}
	if c.Poller.JitterSeconds == 0 {
		c.Poller.JitterSeconds = defaultJitterSeconds
	}
	if c.Poller.CommentFingerprintLimit == 0 {
		c.Poller.CommentFingerprintLimit = defaultCommentFingerprintLimit
	}
	if c.Poller.FileFingerprintLimit == 0 {
		c.Poller.FileFingerprintLimit = defaultFileFingerprintLimit
	}
	if c.Poller.MaxFingerprintBytes == 0 {
		c.Poller.MaxFingerprintBytes = defaultMaxFingerprintBytes
	}
	if c.Poller.MaxPostsPerRun == 0 {
		c.Poller.MaxPostsPerRun = defaultMaxPostsPerRun
	}
	if c.Poller.MaxRunsPerHour == 0 {
		c.Poller.MaxRunsPerHour = defaultMaxRunsPerHour
	}
	if c.Poller.MaxRunsPerDay == 0 {
		c.Poller.MaxRunsPerDay = defaultMaxRunsPerDay
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
			backend.Agents = append([]string(nil), defaultRoles...)
		}
		for i := range backend.Agents {
			backend.Agents[i] = strings.ToLower(strings.TrimSpace(backend.Agents[i]))
		}
		normalizedBackends[normalizedName] = backend
	}
	c.AIBackends = normalizedBackends
	for i := range c.Repos {
		if c.Repos[i].PollIntervalSeconds == 0 {
			c.Repos[i].PollIntervalSeconds = defaultPollIntervalSeconds
		}
		c.Repos[i].FullName = strings.TrimSpace(c.Repos[i].FullName)
	}
}

func (c *Config) resolveEnv() error {
	if c.Database.DSN == "" && c.Database.DSNEnv != "" {
		c.Database.DSN = os.Getenv(c.Database.DSNEnv)
	}
	if c.Database.DSN == "" {
		return errors.New("config: database dsn is required")
	}
	if c.GitHub.Token == "" && c.GitHub.TokenEnv != "" {
		c.GitHub.Token = os.Getenv(c.GitHub.TokenEnv)
	}
	if c.GitHub.Token == "" {
		return errors.New("config: github token is required")
	}
	if c.GitHub.APIBaseURL == "" {
		c.GitHub.APIBaseURL = "https://api.github.com"
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

func (c *Config) MaxAgentTimeoutSeconds() int {
	maxTimeout := 0
	for _, backend := range c.AIBackends {
		if backend.TimeoutSeconds > maxTimeout {
			maxTimeout = backend.TimeoutSeconds
		}
	}
	if maxTimeout == 0 {
		return defaultAITimeoutSeconds
	}
	return maxTimeout
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
