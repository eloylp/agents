package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRequiresSupportedAgentNames(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
agents:
  openai:
    mode: noop
repos:
  - full_name: "owner/repo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail for unsupported agent name")
	}
}

func TestLoadAppliesAgentDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
agents:
  claude:
    mode: noop
repos:
  - full_name: "owner/repo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	agent := cfg.Agents["claude"]
	if agent.TimeoutSeconds != defaultAITimeoutSeconds {
		t.Fatalf("expected timeout default %d, got %d", defaultAITimeoutSeconds, agent.TimeoutSeconds)
	}
	if len(agent.Roles) == 0 {
		t.Fatalf("expected default roles")
	}
}

func TestDefaultConfiguredAgent(t *testing.T) {
	cfg := Config{Agents: map[string]AgentConfig{"codex": {}, "claude": {}}}
	if got := cfg.DefaultConfiguredAgent(); got != "claude" {
		t.Fatalf("expected claude, got %q", got)
	}
	cfg = Config{Agents: map[string]AgentConfig{"codex": {}}}
	if got := cfg.DefaultConfiguredAgent(); got != "codex" {
		t.Fatalf("expected codex, got %q", got)
	}
	cfg = Config{}
	if got := cfg.DefaultConfiguredAgent(); got != "" {
		t.Fatalf("expected empty default agent, got %q", got)
	}
}
