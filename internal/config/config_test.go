package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAIBackendToClaude(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
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
	if cfg.AIBackend != "claude" {
		t.Fatalf("expected default ai_backend=claude, got %q", cfg.AIBackend)
	}
}

func TestLoadRejectsInvalidAIBackend(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
ai_backend: invalid
repos:
  - full_name: "owner/repo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail for invalid ai_backend")
	}
}

func TestLoadOpenAIBackendTimeoutSelection(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
ai_backend: openai
claude:
  timeout_seconds: 111
openai:
  timeout_seconds: 222
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
	if cfg.AIBackend != AIBackendOpenAI {
		t.Fatalf("expected ai_backend=openai, got %q", cfg.AIBackend)
	}
	if got := cfg.AIBackendTimeoutSeconds(); got != 222 {
		t.Fatalf("expected openai timeout 222, got %d", got)
	}
}

func TestLoadOpenAIDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://user:pass@localhost:5432/db")
	t.Setenv("GITHUB_TOKEN", "token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn_env: DATABASE_URL
github:
  token_env: GITHUB_TOKEN
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
	if cfg.OpenAI.Mode != "noop" {
		t.Fatalf("expected openai mode=noop, got %q", cfg.OpenAI.Mode)
	}
	if cfg.OpenAI.TimeoutSeconds != defaultAITimeoutSeconds {
		t.Fatalf("expected openai timeout default %d, got %d", defaultAITimeoutSeconds, cfg.OpenAI.TimeoutSeconds)
	}
	if cfg.OpenAI.MaxPromptChars != defaultMaxPromptChars {
		t.Fatalf("expected openai max_prompt_chars default %d, got %d", defaultMaxPromptChars, cfg.OpenAI.MaxPromptChars)
	}
}
