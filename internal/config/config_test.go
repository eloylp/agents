package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalYAML returns a complete valid config with the given repo override.
// Most tests pass repoBlock="" to use a single enabled repo.
func minimalYAML(repoBlock string) string {
	if repoBlock == "" {
		repoBlock = `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	}
	return `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]

skills:
  architect:
    prompt: "Focus on architecture."

agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
` + repoBlock
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadMinimalConfig(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	path := writeConfig(t, minimalYAML(""))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Daemon.HTTP.ListenAddr; got != defaultHTTPListenAddr {
		t.Errorf("listen_addr default: got %q, want %q", got, defaultHTTPListenAddr)
	}
	if got := cfg.Daemon.HTTP.WebhookSecret; got != "s3cret" {
		t.Errorf("webhook secret not resolved from env: got %q", got)
	}
	if got := cfg.Daemon.MemoryDir; got != defaultMemoryDir {
		t.Errorf("memory_dir default: got %q", got)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "reviewer" {
		t.Errorf("agents: got %+v", cfg.Agents)
	}
	if cfg.Agents[0].Prompt != "You review PRs." {
		t.Errorf("agent prompt not preserved: got %q", cfg.Agents[0].Prompt)
	}
}

func TestLoadResolvesPromptFile(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "reviewer.md")
	if err := os.WriteFile(promptPath, []byte("  review the PR carefully  "), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	content := `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]

skills:
  architect: {prompt: "Focus on architecture."}

agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt_file: reviewer.md

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Agents[0].Prompt; got != "review the PR carefully" {
		t.Errorf("prompt_file not resolved: got %q", got)
	}
}

func TestLoadRejectsMissingSecret(t *testing.T) {
	os.Unsetenv("TEST_SECRET")
	path := writeConfig(t, minimalYAML(""))

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "webhook secret") {
		t.Fatalf("expected webhook secret error, got %v", err)
	}
}

func TestLoadRejectsUnknownSkillRef(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	content := `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]

skills:
  architect: {prompt: "Focus on architecture."}

agents:
  - name: reviewer
    backend: claude
    skills: [nosuch]
    prompt: "You review PRs."

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	_, err := Load(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("expected unknown skill error, got %v", err)
	}
}

func TestLoadRejectsUnknownBackendRef(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	content := `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]

skills:
  architect: {prompt: "Focus on architecture."}

agents:
  - name: reviewer
    backend: codex
    skills: [architect]
    prompt: "You review PRs."

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	_, err := Load(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("expected unknown backend error, got %v", err)
	}
}

func TestLoadRejectsUnknownAgentBinding(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	repo := `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: ghost
        labels: ["ai:review:ghost"]
`
	_, err := Load(writeConfig(t, minimalYAML(repo)))
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("expected unknown agent error, got %v", err)
	}
}

func TestLoadRejectsBindingWithoutTrigger(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	repo := `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
`
	_, err := Load(writeConfig(t, minimalYAML(repo)))
	if err == nil || !strings.Contains(err.Error(), "no trigger") {
		t.Fatalf("expected no-trigger error, got %v", err)
	}
}

func TestLoadRejectsBindingWithMixedLabelsAndEvents(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	repo := `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
        events: ["issues.opened"]
`
	_, err := Load(writeConfig(t, minimalYAML(repo)))
	if err == nil || !strings.Contains(err.Error(), "mixes labels and events") {
		t.Fatalf("expected mixed-trigger error, got %v", err)
	}
}

func TestLoadRejectsAllReposDisabled(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	repo := `
repos:
  - name: "owner/repo"
    enabled: false
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	_, err := Load(writeConfig(t, minimalYAML(repo)))
	if err == nil || !strings.Contains(err.Error(), "must be enabled") {
		t.Fatalf("expected must-be-enabled error, got %v", err)
	}
}

func TestLoadRejectsDuplicateAgent(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	content := `
daemon:
  http: {webhook_secret_env: TEST_SECRET}
  ai_backends:
    claude:
      command: claude

skills:
  architect: {prompt: "Focus on architecture."}

agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "A"
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "B"

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	_, err := Load(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), "duplicate agent") {
		t.Fatalf("expected duplicate agent error, got %v", err)
	}
}

func TestBindingIsEnabledDefaultsTrue(t *testing.T) {
	t.Parallel()
	var b Binding
	if !b.IsEnabled() {
		t.Errorf("expected enabled by default")
	}
	f := false
	b.Enabled = &f
	if b.IsEnabled() {
		t.Errorf("expected disabled when Enabled=false")
	}
}

func TestDefaultBackendPrefersFirstConfigured(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Daemon: DaemonConfig{
			AIBackends: map[string]AIBackendConfig{
				"claude": {Command: "claude"},
				"codex":  {Command: "codex"},
			},
		},
	}
	if got := cfg.DefaultBackend(); got != "claude" {
		t.Errorf("DefaultBackend: got %q, want claude", got)
	}
}

func TestResolveBackend(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Daemon: DaemonConfig{
			AIBackends: map[string]AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
	}
	cases := []struct {
		configured string
		want       string
	}{
		{"", "claude"},
		{"auto", "claude"},
		{"claude", "claude"},
		{"codex", ""},  // not in ai_backends
		{"CLAUDE", "claude"}, // case-folded
	}
	for _, tc := range cases {
		if got := cfg.ResolveBackend(tc.configured); got != tc.want {
			t.Errorf("ResolveBackend(%q): got %q, want %q", tc.configured, got, tc.want)
		}
	}
}

func TestConfigExampleYAMLLoads(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "s3cret")
	t.Setenv("AGENTS_API_KEY", "apikey")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	path := filepath.Join(root, "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("config.example.yaml not present at %s: %v", path, err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if len(cfg.Agents) == 0 {
		t.Fatal("example has no agents")
	}
	if len(cfg.Skills) == 0 {
		t.Fatal("example has no skills")
	}
}

func TestLoadRejectsNegativeDeliveryTTL(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")

	content := `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
    delivery_ttl_seconds: -1
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
skills:
  architect:
    prompt: "Focus on architecture."
agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	path := writeConfig(t, content)
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for negative delivery_ttl_seconds")
	}
}

func logLevelYAML(level string) string {
	return `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
  log:
    level: ` + level + `
skills:
  architect:
    prompt: "Focus on architecture."
agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
}

func logFormatYAML(format string) string {
	return `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
  log:
    format: ` + format + `
skills:
  architect:
    prompt: "Focus on architecture."
agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")

	cases := []struct {
		name       string
		level      string
		wantErrMsg string
	}{
		{
			name:       "typo",
			level:      "debg",
			wantErrMsg: `config: invalid log level "debg" (supported: trace, debug, info, warn, error, fatal, panic, disabled)`,
		},
		{
			name:       "uppercase-invalid",
			level:      "VERBOSE",
			wantErrMsg: `config: invalid log level "verbose" (supported: trace, debug, info, warn, error, fatal, panic, disabled)`,
		},
		{
			name:       "numeric",
			level:      "1",
			wantErrMsg: `config: invalid log level "1" (supported: trace, debug, info, warn, error, fatal, panic, disabled)`,
		},
		{
			name:       "warning-not-accepted",
			level:      "warning",
			wantErrMsg: `config: invalid log level "warning" (supported: trace, debug, info, warn, error, fatal, panic, disabled)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, logLevelYAML(tc.level))
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for log level %q", tc.level)
			}
			if err.Error() != tc.wantErrMsg {
				t.Errorf("error message = %q, want %q", err.Error(), tc.wantErrMsg)
			}
		})
	}
}

func TestLoadAcceptsValidLogLevels(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")

	levels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic", "disabled", "DEBUG", "INFO", "", "\"  debug  \""}
	for _, level := range levels {
		t.Run("level="+level, func(t *testing.T) {
			path := writeConfig(t, logLevelYAML(level))
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error for log level %q: %v", level, err)
			}
		})
	}
}

func TestLoadRejectsInvalidLogFormat(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")

	cases := []struct {
		name       string
		format     string
		wantErrMsg string
	}{
		{
			name:       "unknown-format",
			format:     "yaml",
			wantErrMsg: `config: unknown log format "yaml" (supported: json, text)`,
		},
		{
			name:       "typo",
			format:     "jsn",
			wantErrMsg: `config: unknown log format "jsn" (supported: json, text)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, logFormatYAML(tc.format))
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for log format %q", tc.format)
			}
			if err.Error() != tc.wantErrMsg {
				t.Errorf("error message = %q, want %q", err.Error(), tc.wantErrMsg)
			}
		})
	}
}

func TestLoadAcceptsValidLogFormats(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")

	formats := []string{"json", "text", "JSON", "TEXT", "", "\"  json  \""}
	for _, format := range formats {
		t.Run("format="+format, func(t *testing.T) {
			path := writeConfig(t, logFormatYAML(format))
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error for log format %q: %v", format, err)
			}
		})
	}
}
