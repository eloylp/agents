package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// minimalCfg returns a minimal *config.Config suitable for round-trip tests.
// It mirrors the structure produced by config.Load on a small YAML file and
// has already had defaults applied and secrets resolved (prompt_file is empty;
// prompts are inline).
func minimalCfg() *config.Config {
	enabled := true
	disabled := false
	return &config.Config{
		Daemon: config.DaemonConfig{
			Log: config.LogConfig{Level: "info", Format: "text"},
			HTTP: config.HTTPConfig{
				ListenAddr:             ":8080",
				StatusPath:             "/status",
				WebhookPath:            "/webhooks/github",
				WebhookSecretEnv:       "GITHUB_WEBHOOK_SECRET",
				WebhookSecret:          "secret-value", // resolved; must NOT be stored
				ReadTimeoutSeconds:     15,
				WriteTimeoutSeconds:    15,
				IdleTimeoutSeconds:     60,
				MaxBodyBytes:           1 << 20,
				DeliveryTTLSeconds:     3600,
				ShutdownTimeoutSeconds: 15,
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
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {
					Command:          "claude",
					TimeoutSeconds:   600,
					MaxPromptChars:   12000,
					RedactionSaltEnv: "LOG_SALT",
				},
			},
			Proxy: config.ProxyConfig{
				Enabled: false,
				Path:    "/v1/messages",
				Upstream: config.ProxyUpstreamConfig{
					TimeoutSeconds: 120,
				},
			},
		},
		Skills: map[string]config.SkillDef{
			"architect": {Prompt: "Focus on architecture."},
			"testing":   {Prompt: "Focus on testing."},
		},
		Agents: []config.AgentDef{
			{
				Name:          "coder",
				Backend:       "claude",
				Skills:        []string{"architect", "testing"},
				Prompt:        "You write code.",
				AllowPRs:      true,
				AllowDispatch: true,
				CanDispatch:   []string{"pr-reviewer"},
				Description:   "Implements fixes",
			},
			{
				Name:          "pr-reviewer",
				Backend:       "claude",
				Skills:        []string{"architect"},
				Prompt:        "You review PRs.",
				AllowPRs:      false,
				AllowDispatch: true,
				Description:   "Reviews pull requests",
			},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "coder", Events: []string{"issues.labeled"}, Enabled: &enabled},
					{Agent: "pr-reviewer", Cron: "0 9 * * *", Enabled: &disabled},
					{Agent: "coder", Labels: []string{"ai:review"}},
				},
			},
		},
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestOpenAndMigrate verifies that Open creates a new database and applies all
// migrations without error, and that calling it again on the same file is
// idempotent (migrations are not re-applied).
func TestOpenAndMigrate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db.Close()

	// Second open should not fail — migrations already applied.
	db2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db2.Close()
}

// TestImportLoad verifies the full round-trip: Import writes a config into the
// database, Load reads it back, and the resulting *config.Config matches the
// original on all fields that are persisted.
func TestImportLoad(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	in := minimalCfg()
	if err := store.Import(db, in); err != nil {
		t.Fatalf("import: %v", err)
	}

	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Daemon config checks.
	if out.Daemon.Log.Level != "info" {
		t.Errorf("log.level: got %q, want %q", out.Daemon.Log.Level, "info")
	}
	if out.Daemon.HTTP.ListenAddr != ":8080" {
		t.Errorf("http.listen_addr: got %q, want %q", out.Daemon.HTTP.ListenAddr, ":8080")
	}
	// Resolved secret must NOT be stored — Load returns empty WebhookSecret.
	if out.Daemon.HTTP.WebhookSecret != "" {
		t.Errorf("WebhookSecret should not be persisted, got %q", out.Daemon.HTTP.WebhookSecret)
	}
	// But the env-var name must survive the round-trip.
	if out.Daemon.HTTP.WebhookSecretEnv != "GITHUB_WEBHOOK_SECRET" {
		t.Errorf("WebhookSecretEnv: got %q, want %q", out.Daemon.HTTP.WebhookSecretEnv, "GITHUB_WEBHOOK_SECRET")
	}
	if out.Daemon.Processor.Dispatch.MaxDepth != 3 {
		t.Errorf("dispatch.max_depth: got %d, want 3", out.Daemon.Processor.Dispatch.MaxDepth)
	}
	// Backends.
	if len(out.Daemon.AIBackends) != 1 {
		t.Fatalf("backends: got %d, want 1", len(out.Daemon.AIBackends))
	}
	claude := out.Daemon.AIBackends["claude"]
	if claude.Command != "claude" {
		t.Errorf("backend command: got %q, want %q", claude.Command, "claude")
	}
	// Skills.
	if len(out.Skills) != 2 {
		t.Fatalf("skills: got %d, want 2", len(out.Skills))
	}
	if out.Skills["architect"].Prompt != "Focus on architecture." {
		t.Errorf("skill architect prompt: got %q", out.Skills["architect"].Prompt)
	}

	// Agents.
	if len(out.Agents) != 2 {
		t.Fatalf("agents: got %d, want 2", len(out.Agents))
	}
	var coder config.AgentDef
	for _, a := range out.Agents {
		if a.Name == "coder" {
			coder = a
		}
	}
	if !coder.AllowPRs {
		t.Error("coder.allow_prs: want true")
	}
	if !coder.AllowDispatch {
		t.Error("coder.allow_dispatch: want true")
	}
	if len(coder.CanDispatch) != 1 || coder.CanDispatch[0] != "pr-reviewer" {
		t.Errorf("coder.can_dispatch: got %v, want [pr-reviewer]", coder.CanDispatch)
	}

	// Repos.
	if len(out.Repos) != 1 {
		t.Fatalf("repos: got %d, want 1", len(out.Repos))
	}
	repo := out.Repos[0]
	if repo.Name != "owner/repo" {
		t.Errorf("repo name: got %q, want %q", repo.Name, "owner/repo")
	}
	if !repo.Enabled {
		t.Error("repo.enabled: want true")
	}
	if len(repo.Use) != 3 {
		t.Fatalf("bindings: got %d, want 3", len(repo.Use))
	}

	// First binding: events trigger, enabled=true.
	b0 := repo.Use[0]
	if b0.Agent != "coder" {
		t.Errorf("binding[0].agent: got %q", b0.Agent)
	}
	if len(b0.Events) != 1 || b0.Events[0] != "issues.labeled" {
		t.Errorf("binding[0].events: got %v", b0.Events)
	}
	if !b0.IsEnabled() {
		t.Error("binding[0].enabled: want true")
	}

	// Second binding: cron trigger, enabled=false.
	b1 := repo.Use[1]
	if b1.Cron != "0 9 * * *" {
		t.Errorf("binding[1].cron: got %q", b1.Cron)
	}
	if b1.IsEnabled() {
		t.Error("binding[1].enabled: want false")
	}

	// Third binding: labels, Enabled field absent (nil → IsEnabled true).
	b2 := repo.Use[2]
	if len(b2.Labels) != 1 || b2.Labels[0] != "ai:review" {
		t.Errorf("binding[2].labels: got %v", b2.Labels)
	}
	if !b2.IsEnabled() {
		t.Error("binding[2]: nil enabled should mean enabled")
	}
}

// TestImportIsIdempotent verifies that calling Import twice on the same config
// does not fail and does not duplicate rows.
func TestImportIsIdempotent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	in := minimalCfg()
	if err := store.Import(db, in); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := store.Import(db, in); err != nil {
		t.Fatalf("second import: %v", err)
	}

	counts, err := store.CountFrom(db)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Agents != 2 {
		t.Errorf("agents: got %d, want 2 after idempotent import", counts.Agents)
	}
	if counts.Bindings != 3 {
		t.Errorf("bindings: got %d, want 3 after idempotent import (duplicate rows indicate non-idempotent import)", counts.Bindings)
	}
}

// TestCountFrom verifies that CountFrom returns sensible row counts after an
// import.
func TestCountFrom(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	counts, err := store.CountFrom(db)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Backends != 1 {
		t.Errorf("backends: got %d, want 1", counts.Backends)
	}
	if counts.Skills != 2 {
		t.Errorf("skills: got %d, want 2", counts.Skills)
	}
	if counts.Agents != 2 {
		t.Errorf("agents: got %d, want 2", counts.Agents)
	}
	if counts.Repos != 1 {
		t.Errorf("repos: got %d, want 1", counts.Repos)
	}
	if counts.Bindings != 3 {
		t.Errorf("bindings: got %d, want 3", counts.Bindings)
	}
	summary := counts.String()
	if summary == "" {
		t.Error("String() returned empty")
	}
}

// TestLoadEmptyDatabase verifies that Load on a fresh (empty) database returns
// a descriptive error rather than a zero-value Config.
func TestLoadEmptyDatabase(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	_, err := store.Load(db)
	if err == nil {
		t.Fatal("expected error loading from empty database, got nil")
	}
}

func seedAgent(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if err := store.UpsertBackend(db, "claude", config.AIBackendConfig{
		Command: "claude",
	}); err != nil {
		t.Fatalf("seedAgent backend: %v", err)
	}
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: name, Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("seedAgent %s: %v", name, err)
	}
}

// TestReadWriteMemory verifies the SQLite memory round-trip: writing a string
// and reading it back returns the same content, and reading a non-existent
// entry returns ("", false, time.Time{}, nil).
func TestReadWriteMemory(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedAgent(t, db, "coder")

	// Non-existent agent/repo returns not-found (found=false).
	content, found, mtime, err := store.ReadMemory(db, "coder", "owner/repo")
	if err != nil {
		t.Fatalf("ReadMemory missing row: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing row")
	}
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
	if !mtime.IsZero() {
		t.Fatalf("expected zero mtime for missing row, got %v", mtime)
	}

	// Write and read back; updated_at should be a recent non-zero time.
	before := time.Now().UTC().Add(-time.Second)
	if err := store.WriteMemory(db, "coder", "owner/repo", "## Active PRs\n- PR #1"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	content, found, mtime, err = store.ReadMemory(db, "coder", "owner/repo")
	if err != nil {
		t.Fatalf("ReadMemory after write: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after write")
	}
	if content != "## Active PRs\n- PR #1" {
		t.Fatalf("content mismatch: got %q", content)
	}
	if mtime.IsZero() {
		t.Fatal("expected non-zero mtime after write")
	}
	if mtime.Before(before) {
		t.Fatalf("mtime %v is before write start %v", mtime, before)
	}

	// Overwrite with empty string to clear: row still exists (found=true) but content is "".
	if err := store.WriteMemory(db, "coder", "owner/repo", ""); err != nil {
		t.Fatalf("WriteMemory clear: %v", err)
	}
	content, found, mtime, err = store.ReadMemory(db, "coder", "owner/repo")
	if err != nil {
		t.Fatalf("ReadMemory after clear: %v", err)
	}
	if !found {
		t.Fatal("expected found=true even after clearing content")
	}
	if content != "" {
		t.Fatalf("expected empty content after clear, got %q", content)
	}
	if mtime.IsZero() {
		t.Fatal("expected non-zero mtime even after clearing content")
	}
}

// TestReadWriteMemoryIsolation verifies that different agent/repo combinations
// are stored independently.
func TestReadWriteMemoryIsolation(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedAgent(t, db, "agent-a")
	seedAgent(t, db, "agent-b")

	if err := store.WriteMemory(db, "agent-a", "repo", "mem-A"); err != nil {
		t.Fatalf("WriteMemory A: %v", err)
	}
	if err := store.WriteMemory(db, "agent-b", "repo", "mem-B"); err != nil {
		t.Fatalf("WriteMemory B: %v", err)
	}

	a, _, _, err := store.ReadMemory(db, "agent-a", "repo")
	if err != nil {
		t.Fatalf("ReadMemory A: %v", err)
	}
	if a != "mem-A" {
		t.Errorf("agent-a: got %q, want %q", a, "mem-A")
	}

	b, _, _, err := store.ReadMemory(db, "agent-b", "repo")
	if err != nil {
		t.Fatalf("ReadMemory B: %v", err)
	}
	if b != "mem-B" {
		t.Errorf("agent-b: got %q, want %q", b, "mem-B")
	}
}
