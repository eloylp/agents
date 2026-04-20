package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

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
				AgentsRunPath:          "/agents/run",
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
			MemoryDir: "/var/lib/agents/memory",
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {
					Command:          "claude",
					Args:             []string{"-p", "--output-format", "json"},
					Env:              map[string]string{"MY_VAR": "my_val"},
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

func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db, func() { db.Close() }
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
	db, cleanup := openTestDB(t)
	defer cleanup()

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
	if out.Daemon.MemoryDir != "/var/lib/agents/memory" {
		t.Errorf("memory_dir: got %q, want %q", out.Daemon.MemoryDir, "/var/lib/agents/memory")
	}

	// Backends.
	if len(out.Daemon.AIBackends) != 1 {
		t.Fatalf("backends: got %d, want 1", len(out.Daemon.AIBackends))
	}
	claude := out.Daemon.AIBackends["claude"]
	if claude.Command != "claude" {
		t.Errorf("backend command: got %q, want %q", claude.Command, "claude")
	}
	if len(claude.Args) != 3 {
		t.Errorf("backend args: got %v, want 3 items", claude.Args)
	}
	if claude.Env["MY_VAR"] != "my_val" {
		t.Errorf("backend env MY_VAR: got %q, want %q", claude.Env["MY_VAR"], "my_val")
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
	db, cleanup := openTestDB(t)
	defer cleanup()

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
	db, cleanup := openTestDB(t)
	defer cleanup()

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
	db, cleanup := openTestDB(t)
	defer cleanup()

	_, err := store.Load(db)
	if err == nil {
		t.Fatal("expected error loading from empty database, got nil")
	}
}

// ── Write API ──────────────────────────────────────────────────────────────

// TestPutDeleteAgent exercises the PutAgent / DeleteAgent round-trip. An agent
// written by PutAgent appears in Load results and is gone after DeleteAgent.
func TestPutDeleteAgent(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Prime the database so daemon config is present (Load requires it).
	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Update an existing agent in-place (no FK violation even with bindings).
	updated := config.AgentDef{
		Name:    "coder",
		Backend: "claude",
		Skills:  []string{"testing"},
		Prompt:  "Updated prompt.",
	}
	if err := store.PutAgent(db, updated); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}

	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after put: %v", err)
	}
	var found config.AgentDef
	for _, a := range out.Agents {
		if a.Name == "coder" {
			found = a
		}
	}
	if found.Prompt != "Updated prompt." {
		t.Errorf("updated prompt: got %q, want %q", found.Prompt, "Updated prompt.")
	}
	// Bindings must survive the in-place update.
	if len(out.Repos[0].Use) != 3 {
		t.Errorf("bindings after update: got %d, want 3", len(out.Repos[0].Use))
	}

	// Add a new agent with no bindings so it is safe to delete.
	fresh := config.AgentDef{Name: "temp", Backend: "claude", Prompt: "Temporary."}
	if err := store.PutAgent(db, fresh); err != nil {
		t.Fatalf("PutAgent new: %v", err)
	}
	if err := store.DeleteAgent(db, "temp"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	out2, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	for _, a := range out2.Agents {
		if a.Name == "temp" {
			t.Error("deleted agent still present in load result")
		}
	}
}

// TestPutDeleteSkill exercises the skill write path.
func TestPutDeleteSkill(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Update existing skill.
	if err := store.PutSkill(db, "architect", config.SkillDef{Prompt: "New architect prompt."}); err != nil {
		t.Fatalf("PutSkill update: %v", err)
	}
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.Skills["architect"].Prompt != "New architect prompt." {
		t.Errorf("skill prompt: got %q", out.Skills["architect"].Prompt)
	}

	// Add then delete a new skill.
	if err := store.PutSkill(db, "temp-skill", config.SkillDef{Prompt: "Temp."}); err != nil {
		t.Fatalf("PutSkill new: %v", err)
	}
	if err := store.DeleteSkill(db, "temp-skill"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	out2, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if _, ok := out2.Skills["temp-skill"]; ok {
		t.Error("deleted skill still present")
	}
}

// TestPutDeleteBackend exercises the backend write path.
func TestPutDeleteBackend(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	newBackend := config.AIBackendConfig{
		Command:        "codex",
		Args:           []string{"--model", "gpt-4"},
		Env:            map[string]string{"OPENAI_API_KEY": "x"},
		TimeoutSeconds: 300,
		MaxPromptChars: 8000,
	}
	if err := store.PutBackend(db, "codex", newBackend); err != nil {
		t.Fatalf("PutBackend: %v", err)
	}
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if b, ok := out.Daemon.AIBackends["codex"]; !ok {
		t.Error("codex backend not found after put")
	} else if b.Command != "codex" {
		t.Errorf("backend command: got %q", b.Command)
	}

	if err := store.DeleteBackend(db, "codex"); err != nil {
		t.Fatalf("DeleteBackend: %v", err)
	}
	out2, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if _, ok := out2.Daemon.AIBackends["codex"]; ok {
		t.Error("deleted backend still present")
	}
}

// TestPutDeleteRepo exercises repo creation and deletion. DeleteRepo must
// cascade-delete its bindings.
func TestPutDeleteRepo(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Add a new repo.
	newRepo := config.RepoDef{Name: "owner/other", Enabled: true}
	if err := store.PutRepo(db, newRepo); err != nil {
		t.Fatalf("PutRepo: %v", err)
	}
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after put: %v", err)
	}
	var foundRepo bool
	for _, r := range out.Repos {
		if r.Name == "owner/other" {
			foundRepo = true
		}
	}
	if !foundRepo {
		t.Error("new repo not found after put")
	}

	// Toggle the original repo disabled.
	if err := store.PutRepo(db, config.RepoDef{Name: "owner/repo", Enabled: false}); err != nil {
		t.Fatalf("PutRepo update: %v", err)
	}
	out2, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	for _, r := range out2.Repos {
		if r.Name == "owner/repo" && r.Enabled {
			t.Error("repo should be disabled after update")
		}
	}

	// DeleteRepo must also remove its bindings.
	if err := store.DeleteRepo(db, "owner/repo"); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	out3, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	for _, r := range out3.Repos {
		if r.Name == "owner/repo" {
			t.Error("deleted repo still present")
		}
	}
	counts, _ := store.CountFrom(db)
	if counts.Bindings != 0 {
		t.Errorf("bindings not cascade-deleted: got %d", counts.Bindings)
	}
}

// TestPutDeleteBinding exercises adding and removing individual bindings.
func TestPutDeleteBinding(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.Import(db, minimalCfg()); err != nil {
		t.Fatalf("import: %v", err)
	}

	b := config.Binding{
		Agent:  "pr-reviewer",
		Events: []string{"pull_request.opened"},
	}
	id, err := store.PutBinding(db, "owner/repo", b)
	if err != nil {
		t.Fatalf("PutBinding: %v", err)
	}
	if id == 0 {
		t.Error("PutBinding returned zero ID")
	}

	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after put: %v", err)
	}
	if len(out.Repos[0].Use) != 4 {
		t.Errorf("bindings after add: got %d, want 4", len(out.Repos[0].Use))
	}

	if err := store.DeleteBinding(db, id); err != nil {
		t.Fatalf("DeleteBinding: %v", err)
	}
	out2, err := store.Load(db)
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(out2.Repos[0].Use) != 3 {
		t.Errorf("bindings after delete: got %d, want 3", len(out2.Repos[0].Use))
	}
}
