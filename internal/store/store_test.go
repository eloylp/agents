package store

import (
	"os"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/config"
)

// minimalConfig returns a minimal valid config.Config for use in tests.
// The webhook secret is resolved via t.Setenv so config.Load validation passes
// when roundtripped.
func minimalConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("TEST_WEBHOOK_SECRET", "test-secret")
	t.Setenv("TEST_API_KEY", "test-api-key")
	trueBool := true
	falseBool := false
	return &config.Config{
		Daemon: config.DaemonConfig{
			Log: config.LogConfig{
				Level:  "info",
				Format: "text",
			},
			HTTP: config.HTTPConfig{
				ListenAddr:             ":8080",
				StatusPath:             "/status",
				WebhookPath:            "/webhooks/github",
				AgentsRunPath:          "/agents/run",
				WebhookSecretEnv:       "TEST_WEBHOOK_SECRET",
				APIKeyEnv:              "TEST_API_KEY",
				ReadTimeoutSeconds:     15,
				WriteTimeoutSeconds:    15,
				IdleTimeoutSeconds:     60,
				MaxBodyBytes:           1 << 20,
				DeliveryTTLSeconds:     3600,
				ShutdownTimeoutSeconds: 15,
				// Resolved fields intentionally blank; Finalize resolves them.
			},
			Processor: config.ProcessorConfig{
				EventQueueBuffer:    256,
				MaxConcurrentAgents: 4,
				Dispatch: config.DispatchConfig{
					MaxDepth:           3,
					MaxFanout:          4,
					DedupWindowSeconds: 300,
				},
			},
			MemoryDir: "/var/lib/agents/memory",
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {
					Command:        "claude",
					Args:           []string{"-p", "--dangerously-skip-permissions"},
					Env:            map[string]string{"FOO": "bar"},
					TimeoutSeconds: 600,
					MaxPromptChars: 12000,
				},
			},
		},
		Skills: map[string]config.SkillDef{
			"architect": {Prompt: "Focus on architecture."},
			"testing":   {Prompt: "Write thorough tests."},
		},
		Agents: []config.AgentDef{
			{
				Name:          "reviewer",
				Backend:       "claude",
				Skills:        []string{"architect"},
				Prompt:        "You review PRs carefully.",
				AllowPRs:      false,
				AllowDispatch: true,
				CanDispatch:   []string{},
				Description:   "Reviews pull requests for quality.",
			},
			{
				Name:          "coder",
				Backend:       "claude",
				Skills:        []string{"architect", "testing"},
				Prompt:        "You implement features.",
				AllowPRs:      true,
				AllowDispatch: true,
				CanDispatch:   []string{"reviewer"},
				Description:   "Implements features and fixes bugs.",
			},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "reviewer", Labels: []string{"ai:review"}, Events: nil, Cron: ""},
					{Agent: "coder", Labels: nil, Events: []string{"issues.labeled"}, Cron: ""},
					{Agent: "reviewer", Labels: nil, Events: nil, Cron: "0 * * * *", Enabled: &falseBool},
				},
			},
			{
				Name:    "owner/other",
				Enabled: false,
				Use: []config.Binding{
					{Agent: "reviewer", Labels: nil, Events: nil, Cron: "30 6 * * *", Enabled: &trueBool},
				},
			},
		},
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestRoundTrip verifies that Import followed by LoadConfig produces a
// config.Config that is semantically equivalent to the original input (after
// Finalize normalisation).
func TestRoundTrip(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "s3cret")
	t.Setenv("TEST_API_KEY", "mykey")
	original := minimalConfig(t)

	// Finalize so defaults and normalisation are applied before we compare.
	if err := original.Finalize(); err != nil {
		t.Fatalf("Finalize original: %v", err)
	}

	s := openTestStore(t)
	if err := s.Import(original); err != nil {
		t.Fatalf("Import: %v", err)
	}

	loaded, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Backends
	if got, want := len(loaded.Daemon.AIBackends), len(original.Daemon.AIBackends); got != want {
		t.Errorf("backends count: got %d, want %d", got, want)
	}
	for name, orig := range original.Daemon.AIBackends {
		got, ok := loaded.Daemon.AIBackends[name]
		if !ok {
			t.Errorf("backend %q missing after load", name)
			continue
		}
		if got.Command != orig.Command {
			t.Errorf("backend %s command: got %q, want %q", name, got.Command, orig.Command)
		}
		if len(got.Args) != len(orig.Args) {
			t.Errorf("backend %s args len: got %d, want %d", name, len(got.Args), len(orig.Args))
		}
		for i, a := range orig.Args {
			if i < len(got.Args) && got.Args[i] != a {
				t.Errorf("backend %s args[%d]: got %q, want %q", name, i, got.Args[i], a)
			}
		}
	}

	// Skills
	if got, want := len(loaded.Skills), len(original.Skills); got != want {
		t.Errorf("skills count: got %d, want %d", got, want)
	}
	for name, orig := range original.Skills {
		got, ok := loaded.Skills[name]
		if !ok {
			t.Errorf("skill %q missing after load", name)
			continue
		}
		if got.Prompt != orig.Prompt {
			t.Errorf("skill %s prompt: got %q, want %q", name, got.Prompt, orig.Prompt)
		}
	}

	// Agents
	if got, want := len(loaded.Agents), len(original.Agents); got != want {
		t.Fatalf("agents count: got %d, want %d", got, want)
	}
	agentsByName := make(map[string]config.AgentDef, len(loaded.Agents))
	for _, a := range loaded.Agents {
		agentsByName[a.Name] = a
	}
	for _, orig := range original.Agents {
		got, ok := agentsByName[orig.Name]
		if !ok {
			t.Errorf("agent %q missing after load", orig.Name)
			continue
		}
		if got.Backend != orig.Backend {
			t.Errorf("agent %s backend: got %q, want %q", orig.Name, got.Backend, orig.Backend)
		}
		if got.Prompt != orig.Prompt {
			t.Errorf("agent %s prompt: got %q, want %q", orig.Name, got.Prompt, orig.Prompt)
		}
		if got.AllowPRs != orig.AllowPRs {
			t.Errorf("agent %s allow_prs: got %v, want %v", orig.Name, got.AllowPRs, orig.AllowPRs)
		}
		if got.AllowDispatch != orig.AllowDispatch {
			t.Errorf("agent %s allow_dispatch: got %v, want %v", orig.Name, got.AllowDispatch, orig.AllowDispatch)
		}
		if got.Description != orig.Description {
			t.Errorf("agent %s description: got %q, want %q", orig.Name, got.Description, orig.Description)
		}
	}

	// Repos
	if got, want := len(loaded.Repos), len(original.Repos); got != want {
		t.Fatalf("repos count: got %d, want %d", got, want)
	}
	reposByName := make(map[string]config.RepoDef, len(loaded.Repos))
	for _, r := range loaded.Repos {
		reposByName[r.Name] = r
	}
	for _, orig := range original.Repos {
		got, ok := reposByName[orig.Name]
		if !ok {
			t.Errorf("repo %q missing after load", orig.Name)
			continue
		}
		if got.Enabled != orig.Enabled {
			t.Errorf("repo %s enabled: got %v, want %v", orig.Name, got.Enabled, orig.Enabled)
		}
		if len(got.Use) != len(orig.Use) {
			t.Errorf("repo %s bindings count: got %d, want %d", orig.Name, len(got.Use), len(orig.Use))
			continue
		}
		for i, ob := range orig.Use {
			gb := got.Use[i]
			if gb.Agent != ob.Agent {
				t.Errorf("repo %s binding[%d] agent: got %q, want %q", orig.Name, i, gb.Agent, ob.Agent)
			}
			if gb.Cron != ob.Cron {
				t.Errorf("repo %s binding[%d] cron: got %q, want %q", orig.Name, i, gb.Cron, ob.Cron)
			}
			if gb.IsEnabled() != ob.IsEnabled() {
				t.Errorf("repo %s binding[%d] enabled: got %v, want %v", orig.Name, i, gb.IsEnabled(), ob.IsEnabled())
			}
		}
	}

	// Daemon config
	if got, want := loaded.Daemon.HTTP.ListenAddr, original.Daemon.HTTP.ListenAddr; got != want {
		t.Errorf("http.listen_addr: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.HTTP.WebhookSecretEnv, original.Daemon.HTTP.WebhookSecretEnv; got != want {
		t.Errorf("http.webhook_secret_env: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.HTTP.WebhookSecret, original.Daemon.HTTP.WebhookSecret; got != want {
		t.Errorf("http.webhook_secret resolved: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.Log.Level, original.Daemon.Log.Level; got != want {
		t.Errorf("log.level: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.MemoryDir, original.Daemon.MemoryDir; got != want {
		t.Errorf("memory_dir: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.Processor.Dispatch.MaxDepth, original.Daemon.Processor.Dispatch.MaxDepth; got != want {
		t.Errorf("dispatch.max_depth: got %d, want %d", got, want)
	}
}

// TestImportIsIdempotent verifies that calling Import twice replaces data
// rather than appending.
func TestImportIsIdempotent(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "s3cret")

	s := openTestStore(t)
	cfg := minimalConfig(t)
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if err := s.Import(cfg); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if err := s.Import(cfg); err != nil {
		t.Fatalf("second Import: %v", err)
	}

	loaded, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig after double import: %v", err)
	}
	if got, want := len(loaded.Agents), len(cfg.Agents); got != want {
		t.Errorf("agents after double import: got %d, want %d", got, want)
	}
}

// TestSecretsNotPersisted verifies that resolved secret values are never
// written to the database; only the env-var names are stored.
func TestSecretsNotPersisted(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "super-secret-value")
	t.Setenv("TEST_API_KEY", "api-key-value")

	s := openTestStore(t)
	cfg := minimalConfig(t)
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if err := s.Import(cfg); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Read the raw stored JSON to ensure secret values are absent.
	var httpJSON string
	if err := s.db.QueryRow(`SELECT value FROM config WHERE key='http'`).Scan(&httpJSON); err != nil {
		t.Fatalf("query config http: %v", err)
	}
	if strings.Contains(httpJSON, "super-secret-value") {
		t.Errorf("webhook secret value leaked into config table: %s", httpJSON)
	}
	if strings.Contains(httpJSON, "api-key-value") {
		t.Errorf("api key value leaked into config table: %s", httpJSON)
	}
}

// TestOpenInvalidPath verifies that Open returns an error for an unwritable
// path.
func TestOpenInvalidPath(t *testing.T) {
	t.Parallel()
	_, err := Open("/nonexistent/dir/agents.db")
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

// TestLoadConfigFromYAMLRoundTrip exercises the full path: YAML → config.Load
// → store.Import → store.LoadConfig → compare.
func TestLoadConfigFromYAMLRoundTrip(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "yaml-secret")

	dir := t.TempDir()
	yamlPath := dir + "/config.yaml"
	content := `
daemon:
  http:
    webhook_secret_env: WEBHOOK_SECRET
  ai_backends:
    claude:
      command: claude
      args: ["-p"]
  memory_dir: /tmp/memory

skills:
  architect:
    prompt: "Think about architecture."

agents:
  - name: reviewer
    backend: claude
    skills: [architect]
    prompt: "You review pull requests."

repos:
  - name: owner/repo
    enabled: true
    use:
      - agent: reviewer
        labels: ["ai:review"]
`
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	yamlCfg, err := config.Load(yamlPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	s := openTestStore(t)
	if err := s.Import(yamlCfg); err != nil {
		t.Fatalf("Import: %v", err)
	}

	loaded, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if got, want := loaded.Daemon.MemoryDir, "/tmp/memory"; got != want {
		t.Errorf("memory_dir: got %q, want %q", got, want)
	}
	if got, want := loaded.Daemon.HTTP.WebhookSecret, "yaml-secret"; got != want {
		t.Errorf("webhook secret: got %q, want %q", got, want)
	}
	if got, want := len(loaded.Agents), 1; got != want {
		t.Errorf("agents: got %d, want %d", got, want)
	}
	if got, want := loaded.Agents[0].Name, "reviewer"; got != want {
		t.Errorf("agent name: got %q, want %q", got, want)
	}
	if got, want := len(loaded.Skills), 1; got != want {
		t.Errorf("skills: got %d, want %d", got, want)
	}
}

