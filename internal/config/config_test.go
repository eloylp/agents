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
