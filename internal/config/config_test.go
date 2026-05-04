package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
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
backends:
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
	path := writeConfig(t, minimalYAML(""))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Daemon.HTTP.ListenAddr; got != defaultHTTPListenAddr {
		t.Errorf("listen_addr default: got %q, want %q", got, defaultHTTPListenAddr)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "reviewer" {
		t.Errorf("agents: got %+v", cfg.Agents)
	}
	if cfg.Agents[0].Prompt != "You review PRs." {
		t.Errorf("agent prompt not preserved: got %q", cfg.Agents[0].Prompt)
	}
}

func TestLoadAppliesDaemonEnvOverrides(t *testing.T) {
	t.Setenv("LLM_KEY", "key-abc")
	t.Setenv("AGENTS_LOG_LEVEL", "debug")
	t.Setenv("AGENTS_LOG_FORMAT", "json")
	hash := sha256.Sum256([]byte("secret-token"))
	t.Setenv("AGENTS_AUTH_BEARER_TOKEN_HASH", hex.EncodeToString(hash[:]))
	t.Setenv("AGENTS_HTTP_LISTEN_ADDR", "127.0.0.1:9090")
	t.Setenv("AGENTS_HTTP_STATUS_PATH", "/healthz")
	t.Setenv("AGENTS_HTTP_WEBHOOK_PATH", "/hooks/github")
	t.Setenv("AGENTS_HTTP_READ_TIMEOUT_SECONDS", "21")
	t.Setenv("AGENTS_HTTP_WRITE_TIMEOUT_SECONDS", "22")
	t.Setenv("AGENTS_HTTP_IDLE_TIMEOUT_SECONDS", "23")
	t.Setenv("AGENTS_HTTP_MAX_BODY_BYTES", "2048")
	t.Setenv("AGENTS_HTTP_DELIVERY_TTL_SECONDS", "24")
	t.Setenv("AGENTS_HTTP_SHUTDOWN_TIMEOUT_SECONDS", "25")
	t.Setenv("AGENTS_PROCESSOR_EVENT_QUEUE_BUFFER", "26")
	t.Setenv("AGENTS_PROCESSOR_MAX_CONCURRENT_AGENTS", "27")
	t.Setenv("AGENTS_DISPATCH_MAX_DEPTH", "28")
	t.Setenv("AGENTS_DISPATCH_MAX_FANOUT", "29")
	t.Setenv("AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS", "30")
	t.Setenv("AGENTS_PROXY_ENABLED", "true")
	t.Setenv("AGENTS_PROXY_PATH", "/proxy/messages")
	t.Setenv("AGENTS_PROXY_UPSTREAM_URL", "http://localhost:8001/v1")
	t.Setenv("AGENTS_PROXY_UPSTREAM_MODEL", "qwen")
	t.Setenv("AGENTS_PROXY_UPSTREAM_API_KEY_ENV", "LLM_KEY")
	t.Setenv("AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS", "31")
	path := writeConfig(t, minimalYAML(""))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Daemon.Log.Level != "debug" || cfg.Daemon.Log.Format != "json" {
		t.Fatalf("log overrides = %+v, want debug/json", cfg.Daemon.Log)
	}
	if cfg.Daemon.Auth.BearerTokenHash != hex.EncodeToString(hash[:]) {
		t.Fatalf("auth bearer token hash = %q, want env override", cfg.Daemon.Auth.BearerTokenHash)
	}
	if got := cfg.Daemon.HTTP.ListenAddr; got != "127.0.0.1:9090" {
		t.Fatalf("listen addr = %q, want env override", got)
	}
	if got := cfg.Daemon.HTTP.StatusPath; got != "/healthz" {
		t.Fatalf("status path = %q, want env override", got)
	}
	if got := cfg.Daemon.HTTP.WebhookPath; got != "/hooks/github" {
		t.Fatalf("webhook path = %q, want env override", got)
	}
	if h := cfg.Daemon.HTTP; h.ReadTimeoutSeconds != 21 ||
		h.WriteTimeoutSeconds != 22 ||
		h.IdleTimeoutSeconds != 23 ||
		h.MaxBodyBytes != 2048 ||
		h.DeliveryTTLSeconds != 24 ||
		h.ShutdownTimeoutSeconds != 25 {
		t.Fatalf("http overrides = %+v", h)
	}
	if p := cfg.Daemon.Processor; p.EventQueueBuffer != 26 ||
		p.MaxConcurrentAgents != 27 ||
		p.Dispatch.MaxDepth != 28 ||
		p.Dispatch.MaxFanout != 29 ||
		p.Dispatch.DedupWindowSeconds != 30 {
		t.Fatalf("processor overrides = %+v", p)
	}
	if p := cfg.Daemon.Proxy; !p.Enabled ||
		p.Path != "/proxy/messages" ||
		p.Upstream.URL != "http://localhost:8001/v1" ||
		p.Upstream.Model != "qwen" ||
		p.Upstream.APIKeyEnv != "LLM_KEY" ||
		p.Upstream.APIKey != "key-abc" ||
		p.Upstream.TimeoutSeconds != 31 {
		t.Fatalf("proxy overrides = %+v", p)
	}
}

func TestLoadRejectsInvalidDaemonEnvOverride(t *testing.T) {
	t.Setenv("AGENTS_HTTP_READ_TIMEOUT_SECONDS", "not-a-number")
	path := writeConfig(t, minimalYAML(""))

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load got nil error, want invalid env error")
	}
	if !strings.Contains(err.Error(), "AGENTS_HTTP_READ_TIMEOUT_SECONDS") {
		t.Fatalf("error = %q, want env var name", err)
	}
}

func TestLoadRejectsInvalidDaemonPathEnvOverride(t *testing.T) {
	t.Setenv("AGENTS_HTTP_STATUS_PATH", "healthz")
	path := writeConfig(t, minimalYAML(""))

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load got nil error, want invalid path env error")
	}
	if !strings.Contains(err.Error(), "AGENTS_HTTP_STATUS_PATH") {
		t.Fatalf("error = %q, want env var name", err)
	}
}

func TestLoadRejectsInvalidAuthBearerTokenHashEnv(t *testing.T) {
	t.Setenv("AGENTS_AUTH_BEARER_TOKEN_HASH", "not-hex")
	path := writeConfig(t, minimalYAML(""))

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load got nil error, want invalid auth hash error")
	}
	if !strings.Contains(err.Error(), "auth bearer token hash") {
		t.Fatalf("error = %q, want auth hash context", err)
	}
}

// agentConfigYAML builds a full config YAML with a custom agents block,
// mirroring minimalYAML but allowing the agents section to be overridden.
func agentConfigYAML(agentsBlock string) string {
	return `
backends:
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

// TestLoadAcceptsAllReposDisabled verifies that a config with every repo
// disabled loads successfully. Disabling all repos is a legitimate user action
// (fleet maintenance, prompt evaluation on a different repo) and the daemon
// must not crash-loop on the next restart. Regression for issue #302.
func TestLoadAcceptsAllReposDisabled(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")
	repo := `
repos:
  - name: "owner/repo"
    enabled: false
    use:
      - agent: reviewer
        labels: ["ai:review:reviewer"]
`
	cfg, err := Load(writeConfig(t, minimalYAML(repo)))
	if err != nil {
		t.Fatalf("Load with all repos disabled: want success, got %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos: got %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Enabled {
		t.Errorf("repo enabled: got true, want false")
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
	var b fleet.Binding
	if !b.IsEnabled() {
		t.Errorf("expected enabled by default")
	}
	f := false
	b.Enabled = &f
	if b.IsEnabled() {
		t.Errorf("expected disabled when Enabled=false")
	}
}

func TestResolveBackend(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Daemon: DaemonConfig{
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
	}
	cases := []struct {
		configured string
		want       string
	}{
		{"", ""},
		{"auto", ""},
		{"claude", "claude"},
		{"codex", ""},        // not in backends
		{"CLAUDE", "claude"}, // case-folded
	}
	for _, tc := range cases {
		if got := cfg.ResolveBackend(tc.configured); got != tc.want {
			t.Errorf("ResolveBackend(%q): got %q, want %q", tc.configured, got, tc.want)
		}
	}
}

func TestLoadRejectsNegativeDeliveryTTL(t *testing.T) {
	t.Setenv("AGENTS_HTTP_DELIVERY_TTL_SECONDS", "-1")
	path := writeConfig(t, minimalYAML(""))
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for negative delivery_ttl_seconds")
	}
}

func logConfigYAML(field, value string) string {
	return `
backends:
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
			t.Setenv("AGENTS_LOG_LEVEL", tc.level)
			path := writeConfig(t, minimalYAML(""))
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
		{field: "level", value: "  debug  "},
		{field: "format", value: "json"},
		{field: "format", value: "text"},
		{field: "format", value: "JSON"},
		{field: "format", value: "TEXT"},
		{field: "format", value: ""},
		{field: "format", value: "  json  "},
	}
	for _, tc := range tests {
		t.Run(tc.field+"="+tc.value, func(t *testing.T) {
			if tc.field == "level" {
				t.Setenv("AGENTS_LOG_LEVEL", tc.value)
			} else {
				t.Setenv("AGENTS_LOG_FORMAT", tc.value)
			}
			path := writeConfig(t, minimalYAML(""))
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error for log %s %q: %v", tc.field, tc.value, err)
			}
		})
	}
}

func TestLoadRejectsInvalidLogFormat(t *testing.T) {
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
			t.Setenv("AGENTS_LOG_FORMAT", tc.format)
			path := writeConfig(t, minimalYAML(""))
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
backends:
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
			errMsg: `agent "pr-reviewer" is in a can_dispatch list but has no description`,
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
		envKey string
		envVal string
		errMsg string
	}{
		{
			name:   "negative max_depth",
			envKey: "AGENTS_DISPATCH_MAX_DEPTH",
			envVal: "-1",
			errMsg: "AGENTS_DISPATCH_MAX_DEPTH",
		},
		{
			name:   "negative max_fanout",
			envKey: "AGENTS_DISPATCH_MAX_FANOUT",
			envVal: "-2",
			errMsg: "AGENTS_DISPATCH_MAX_FANOUT",
		},
		{
			name:   "negative dedup_window_seconds",
			envKey: "AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS",
			envVal: "-5",
			errMsg: "AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envKey, tc.envVal)
			path := writeConfig(t, minimalYAML(""))
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
	t.Setenv("LLM_KEY", "key-abc")
	t.Setenv("AGENTS_PROXY_ENABLED", "true")
	t.Setenv("AGENTS_PROXY_UPSTREAM_URL", "http://localhost:8001/v1")
	t.Setenv("AGENTS_PROXY_UPSTREAM_MODEL", "qwen")
	t.Setenv("AGENTS_PROXY_UPSTREAM_API_KEY_ENV", "LLM_KEY")
	t.Setenv("AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS", "60")
	path := writeConfig(t, minimalYAML(""))
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
	tests := []struct {
		name       string
		env        map[string]string
		wantErrMsg string
	}{
		{
			name: "missing url",
			env: map[string]string{
				"AGENTS_PROXY_ENABLED":        "true",
				"AGENTS_PROXY_UPSTREAM_MODEL": "qwen",
			},
			wantErrMsg: "config: proxy.upstream.url is required when proxy.enabled is true",
		},
		{
			name: "missing model",
			env: map[string]string{
				"AGENTS_PROXY_ENABLED":      "true",
				"AGENTS_PROXY_UPSTREAM_URL": "http://localhost:8001/v1",
			},
			wantErrMsg: "config: proxy.upstream.model is required when proxy.enabled is true",
		},
		{
			name: "path without slash",
			env: map[string]string{
				"AGENTS_PROXY_ENABLED":        "true",
				"AGENTS_PROXY_PATH":           "v1/messages",
				"AGENTS_PROXY_UPSTREAM_URL":   "http://localhost:8001/v1",
				"AGENTS_PROXY_UPSTREAM_MODEL": "qwen",
			},
			wantErrMsg: `config: AGENTS_PROXY_PATH must start with '/', got "v1/messages"`,
		},
		{
			name: "non-positive timeout",
			env: map[string]string{
				"AGENTS_PROXY_ENABLED":                  "true",
				"AGENTS_PROXY_UPSTREAM_URL":             "http://localhost:8001/v1",
				"AGENTS_PROXY_UPSTREAM_MODEL":           "qwen",
				"AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS": "-1",
			},
			wantErrMsg: `config: AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS must be a positive integer, got "-1"`,
		},
		{
			name: "api_key_env set but variable unset",
			env: map[string]string{
				"AGENTS_PROXY_ENABLED":              "true",
				"AGENTS_PROXY_UPSTREAM_URL":         "http://localhost:8001/v1",
				"AGENTS_PROXY_UPSTREAM_MODEL":       "qwen",
				"AGENTS_PROXY_UPSTREAM_API_KEY_ENV": "MISSING_LLM_KEY",
			},
			wantErrMsg: `config: proxy.upstream.api_key_env "MISSING_LLM_KEY" is set but the environment variable is empty or unset`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := writeConfig(t, minimalYAML(""))
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
