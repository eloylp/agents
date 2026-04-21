package config

import (
	"fmt"
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

// agentConfigYAML builds a full config YAML with a custom agents block,
// mirroring minimalYAML but allowing the agents section to be overridden.
func agentConfigYAML(agentsBlock string) string {
	return `
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
` + agentsBlock + `

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
}

func TestLoadRejectsInvalidAgentConfig(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	tests := []struct {
		name       string
		agents     string
		wantErrMsg string
	}{
		{
			name: "unknown skill reference",
			agents: `  - name: reviewer
    backend: claude
    skills: [nosuch]
    prompt: "You review PRs."`,
			wantErrMsg: "unknown skill",
		},
		{
			name: "unknown backend reference",
			agents: `  - name: reviewer
    backend: codex
    skills: [architect]
    prompt: "You review PRs."`,
			wantErrMsg: "unknown backend",
		},
		{
			name: `duplicate agent name`,
			agents: `  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "A"
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "B"`,
			wantErrMsg: "duplicate agent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(writeConfig(t, agentConfigYAML(tc.agents)))
			if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrMsg, err)
			}
		})
	}
}

func TestLoadRejectsInvalidRepoConfig(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	cases := []struct {
		name   string
		repo   string
		errMsg string
	}{
		{
			name: "unknown agent binding",
			repo: `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: ghost
        labels: ["ai:review:ghost"]
`,
			errMsg: "unknown agent",
		},
		{
			name: "binding without trigger",
			repo: `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
`,
			errMsg: "no trigger",
		},
		{
			name: "unknown event kind",
			repo: `
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        events: ["issue_comments.created"]
`,
			errMsg: "unknown event kind",
		},
		{
			name: "all repos disabled",
			repo: `
repos:
  - name: "owner/repo"
    enabled: false
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`,
			errMsg: "must be enabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(writeConfig(t, minimalYAML(tc.repo)))
			if err == nil || !strings.Contains(err.Error(), tc.errMsg) {
				t.Fatalf("expected error containing %q, got %v", tc.errMsg, err)
			}
		})
	}
}

func TestLoadRejectsBindingWithMixedTriggers(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	cases := []struct {
		name string
		use  string
	}{
		{
			name: "labels and events",
			use: `      - agent: reviewer
        labels: ["ai:review"]
        events: ["issues.opened"]`,
		},
		{
			name: "cron and events",
			use: `      - agent: reviewer
        cron: "0 * * * *"
        events: ["issues.opened"]`,
		},
		{
			name: "cron and labels",
			use: `      - agent: reviewer
        cron: "0 * * * *"
        labels: ["ai:review"]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := fmt.Sprintf(`
repos:
  - name: "owner/repo"
    enabled: true
    use:
%s
`, tc.use)
			_, err := Load(writeConfig(t, minimalYAML(repo)))
			if err == nil || !strings.Contains(err.Error(), "mixes multiple trigger types") {
				t.Fatalf("expected mixed-trigger error, got %v", err)
			}
		})
	}
}

func TestLoadAcceptsValidEventKinds(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	for kind := range validEventKinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			repo := fmt.Sprintf(`
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        events: [%q]
`, kind)
			_, err := Load(writeConfig(t, minimalYAML(repo)))
			if err != nil {
				t.Fatalf("kind %q should be valid, got: %v", kind, err)
			}
		})
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

func logConfigYAML(field, value string) string {
	return `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
  log:
    ` + field + `: ` + value + `
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
			t.Parallel()
			path := writeConfig(t, logConfigYAML("level", tc.level))
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

func TestLoadAcceptsValidLogConfig(t *testing.T) {
	t.Setenv("TEST_SECRET", "secret")
	tests := []struct {
		field string
		value string
	}{
		{field: "level", value: "trace"},
		{field: "level", value: "debug"},
		{field: "level", value: "info"},
		{field: "level", value: "warn"},
		{field: "level", value: "error"},
		{field: "level", value: "fatal"},
		{field: "level", value: "panic"},
		{field: "level", value: "disabled"},
		{field: "level", value: "DEBUG"},
		{field: "level", value: "INFO"},
		{field: "level", value: ""},
		{field: "level", value: `"  debug  "`},
		{field: "format", value: "json"},
		{field: "format", value: "text"},
		{field: "format", value: "JSON"},
		{field: "format", value: "TEXT"},
		{field: "format", value: ""},
		{field: "format", value: `"  json  "`},
	}
	for _, tc := range tests {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, logConfigYAML(tc.field, tc.value))
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error for log %s %q: %v", tc.field, tc.value, err)
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
			t.Parallel()
			path := writeConfig(t, logConfigYAML("format", tc.format))
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

// ─── dispatch wiring validation ───────────────────────────────────────────────

func dispatchYAML(agentBlock string) string {
	return fmt.Sprintf(`
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
%s

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: coder
        labels: ["ai:code"]
`, agentBlock)
}

func TestDispatchWiringValidationErrors(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	cases := []struct {
		name   string
		agents string
		errMsg string
	}{
		{
			name: "unknown agent in can_dispatch",
			agents: `
  - name: coder
    backend: claude
    skills: [architect]
    prompt: "Write code."
    can_dispatch: [nonexistent-agent]
`,
			errMsg: "unknown agent",
		},
		{
			name: "can_dispatch includes self",
			agents: `
  - name: coder
    backend: claude
    skills: [architect]
    prompt: "Write code."
    can_dispatch: [coder]
`,
			errMsg: "itself",
		},
		{
			name: "dispatch target missing description",
			agents: `
  - name: coder
    backend: claude
    skills: [architect]
    prompt: "Write code."
    can_dispatch: [pr-reviewer]
  - name: pr-reviewer
    backend: claude
    skills: [architect]
    prompt: "Review PRs."
    allow_dispatch: true
`,
			errMsg: "description",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, dispatchYAML(tc.agents))
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errMsg)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}

func TestDispatchValidConfigAccepted(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	yaml := dispatchYAML(`
  - name: coder
    backend: claude
    skills: [architect]
    prompt: "Write code."
    can_dispatch: [pr-reviewer]
  - name: pr-reviewer
    backend: claude
    skills: [architect]
    prompt: "Review PRs."
    description: "Reviews pull requests for quality and correctness"
    allow_dispatch: true
`)
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	coder, ok := cfg.AgentByName("coder")
	if !ok {
		t.Fatal("coder not found")
	}
	if len(coder.CanDispatch) != 1 || coder.CanDispatch[0] != "pr-reviewer" {
		t.Errorf("can_dispatch not normalized: %v", coder.CanDispatch)
	}
	reviewer, ok := cfg.AgentByName("pr-reviewer")
	if !ok {
		t.Fatal("pr-reviewer not found")
	}
	if !reviewer.AllowDispatch {
		t.Error("allow_dispatch should be true for pr-reviewer")
	}
	if reviewer.Description == "" {
		t.Error("description should be set for pr-reviewer")
	}
}

func TestDispatchConfigValidationRejectsNonPositiveValues(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	cases := []struct {
		name   string
		yaml   string
		errMsg string
	}{
		{
			name: "negative max_depth",
			yaml: `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
  processor:
    dispatch:
      max_depth: -1
agents:
  - name: reviewer
    backend: claude
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
`,
			errMsg: "max_depth",
		},
		{
			name: "negative max_fanout",
			yaml: `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
  processor:
    dispatch:
      max_fanout: -2
agents:
  - name: reviewer
    backend: claude
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
`,
			errMsg: "max_fanout",
		},
		{
			name: "negative dedup_window_seconds",
			yaml: `
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
  processor:
    dispatch:
      dedup_window_seconds: -5
agents:
  - name: reviewer
    backend: claude
    prompt: "You review PRs."
repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
`,
			errMsg: "dedup_window_seconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, tc.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errMsg)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}

func TestDispatchDefaultsApplied(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	path := writeConfig(t, minimalYAML(""))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := cfg.Daemon.Processor.Dispatch
	if d.MaxDepth != 3 {
		t.Errorf("MaxDepth default: got %d, want 3", d.MaxDepth)
	}
	if d.MaxFanout != 4 {
		t.Errorf("MaxFanout default: got %d, want 4", d.MaxFanout)
	}
	if d.DedupWindowSeconds != 300 {
		t.Errorf("DedupWindowSeconds default: got %d, want 300", d.DedupWindowSeconds)
	}
}

// ── proxy config ───────────────────────────────────────────────────────────────

func proxyYAML(proxyBlock string) string {
	return fmt.Sprintf(`
daemon:
  http:
    webhook_secret_env: TEST_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
  proxy:
%s

agents:
  - name: reviewer
    backend: claude
    prompt: "You review PRs."

repos:
  - name: "owner/repo"
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
`, proxyBlock)
}

func TestProxyDisabledByDefault(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	path := writeConfig(t, minimalYAML(""))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Proxy.Enabled {
		t.Error("proxy should be disabled by default")
	}
}

func TestProxyDefaultsApplied(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	path := writeConfig(t, minimalYAML(""))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Proxy.Path != defaultProxyPath {
		t.Errorf("proxy path default: got %q, want %q", cfg.Daemon.Proxy.Path, defaultProxyPath)
	}
	if cfg.Daemon.Proxy.Upstream.TimeoutSeconds != defaultProxyTimeoutSeconds {
		t.Errorf("proxy timeout default: got %d, want %d", cfg.Daemon.Proxy.Upstream.TimeoutSeconds, defaultProxyTimeoutSeconds)
	}
}

func TestProxyValidConfigLoads(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	t.Setenv("LLM_KEY", "key-abc")
	block := `
    enabled: true
    upstream:
      url: http://localhost:8001/v1
      model: qwen
      api_key_env: LLM_KEY
      timeout_seconds: 60`
	path := writeConfig(t, proxyYAML(block))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Daemon.Proxy.Enabled {
		t.Error("proxy not enabled")
	}
	if cfg.Daemon.Proxy.Upstream.URL != "http://localhost:8001/v1" {
		t.Errorf("upstream url: got %q", cfg.Daemon.Proxy.Upstream.URL)
	}
	if cfg.Daemon.Proxy.Upstream.Model != "qwen" {
		t.Errorf("upstream model: got %q", cfg.Daemon.Proxy.Upstream.Model)
	}
	if cfg.Daemon.Proxy.Upstream.APIKey != "key-abc" {
		t.Errorf("upstream api key not resolved: got %q", cfg.Daemon.Proxy.Upstream.APIKey)
	}
	if cfg.Daemon.Proxy.Upstream.TimeoutSeconds != 60 {
		t.Errorf("upstream timeout: got %d", cfg.Daemon.Proxy.Upstream.TimeoutSeconds)
	}
}

func TestProxyValidationErrors(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")

	tests := []struct {
		name       string
		block      string
		wantErrMsg string
	}{
		{
			name: "missing url",
			block: `
    enabled: true
    upstream:
      model: qwen`,
			wantErrMsg: "config: proxy.upstream.url is required when proxy.enabled is true",
		},
		{
			name: "missing model",
			block: `
    enabled: true
    upstream:
      url: http://localhost:8001/v1`,
			wantErrMsg: "config: proxy.upstream.model is required when proxy.enabled is true",
		},
		{
			name: "path without slash",
			block: `
    enabled: true
    path: v1/messages
    upstream:
      url: http://localhost:8001/v1
      model: qwen`,
			wantErrMsg: `config: proxy.path must start with '/', got "v1/messages"`,
		},
		{
			name: "non-positive timeout",
			block: `
    enabled: true
    upstream:
      url: http://localhost:8001/v1
      model: qwen
      timeout_seconds: -1`,
			wantErrMsg: "config: proxy.upstream.timeout_seconds must be positive, got -1",
		},
		{
			name: "api_key_env set but variable unset",
			block: `
    enabled: true
    upstream:
      url: http://localhost:8001/v1
      model: qwen
      api_key_env: MISSING_LLM_KEY`,
			wantErrMsg: `config: proxy.upstream.api_key_env "MISSING_LLM_KEY" is set but the environment variable is empty or unset`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, proxyYAML(tc.block))
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tc.wantErrMsg {
				t.Errorf("error: got %q, want %q", err.Error(), tc.wantErrMsg)
			}
		})
	}
}
