package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRequiresSupportedAgentNames(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
	unsupported:
    mode: noop
repos:
  - full_name: "owner/repo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail for unsupported ai backend name")
	}
}

func TestLoadAppliesAgentDefaults(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `http:
  webhook_secret_env: WEBHOOK_SECRET
ai_backends:
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
	backend := cfg.AIBackends["claude"]
	if backend.TimeoutSeconds != defaultAITimeoutSeconds {
		t.Fatalf("expected timeout default %d, got %d", defaultAITimeoutSeconds, backend.TimeoutSeconds)
	}
	if len(backend.Agents) == 0 {
		t.Fatalf("expected default specialist agents")
	}
	if cfg.HTTP.IssueQueueBuffer != defaultIssueQueueBufferSize {
		t.Fatalf("expected issue queue buffer default %d, got %d", defaultIssueQueueBufferSize, cfg.HTTP.IssueQueueBuffer)
	}
	if cfg.HTTP.PRQueueBuffer != defaultPRQueueBufferSize {
		t.Fatalf("expected pr queue buffer default %d, got %d", defaultPRQueueBufferSize, cfg.HTTP.PRQueueBuffer)
	}
	if cfg.HTTP.ShutdownTimeoutSeconds != defaultHTTPShutdownSeconds {
		t.Fatalf("expected shutdown timeout default %d, got %d", defaultHTTPShutdownSeconds, cfg.HTTP.ShutdownTimeoutSeconds)
	}
}

func TestDefaultConfiguredBackend(t *testing.T) {
	cfg := Config{AIBackends: map[string]AIBackendConfig{"codex": {}, "claude": {}}}
	if got := cfg.DefaultConfiguredBackend(); got != "claude" {
		t.Fatalf("expected claude, got %q", got)
	}
	cfg = Config{AIBackends: map[string]AIBackendConfig{"codex": {}}}
	if got := cfg.DefaultConfiguredBackend(); got != "codex" {
		t.Fatalf("expected codex, got %q", got)
	}
	cfg = Config{}
	if got := cfg.DefaultConfiguredBackend(); got != "" {
		t.Fatalf("expected empty default backend, got %q", got)
	}
}
