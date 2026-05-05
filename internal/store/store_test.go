package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// minimalCfg returns a minimal *config.Config suitable for round-trip tests.
// It mirrors the structure produced by config.Load on a small YAML file:
// defaults applied, secrets resolved, prompts inline.
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
			AIBackends: map[string]fleet.Backend{
				"claude": {
					Command:        "claude",
					TimeoutSeconds: 600,
					MaxPromptChars: 12000,
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
		Skills: map[string]fleet.Skill{
			"architect": {Prompt: "Focus on architecture."},
			"testing":   {Prompt: "Focus on testing."},
		},
		Agents: []fleet.Agent{
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
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
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

// TestGuardrailsSeed verifies that migrations 010, 012, 013, and 016 created
// the generic guardrails table and seeded the built-in rows with content
// equal to default_content (so a "Reset to default" from the unedited
// state is a no-op), is_builtin=1, enabled=1, and the expected position.
func TestGuardrailsSeed(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	var (
		count                   int
		isBuiltin, enabled, pos int
		content, defaultContent sql.NullString
		description             sql.NullString
		updatedAt               string
	)
	if err := db.QueryRow("SELECT COUNT(*) FROM guardrails").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Fatalf("row count after migrations: got %d, want 4 (security + discretion + memory-scope + mcp-tool-usage)", count)
	}
	if err := db.QueryRow(
		"SELECT description, content, default_content, is_builtin, enabled, position, updated_at FROM guardrails WHERE name = 'security'",
	).Scan(&description, &content, &defaultContent, &isBuiltin, &enabled, &pos, &updatedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !description.Valid || description.String == "" {
		t.Error("description is empty")
	}
	if !content.Valid || content.String == "" {
		t.Error("content is empty")
	}
	if !defaultContent.Valid {
		t.Error("default_content is NULL on built-in row")
	}
	if content.String != defaultContent.String {
		t.Error("content and default_content diverge on first migration")
	}
	if isBuiltin != 1 {
		t.Errorf("is_builtin: got %d, want 1", isBuiltin)
	}
	if enabled != 1 {
		t.Errorf("enabled: got %d, want 1", enabled)
	}
	if pos != 0 {
		t.Errorf("position: got %d, want 0", pos)
	}
	if updatedAt == "" {
		t.Error("updated_at is empty")
	}
	for _, want := range []string{"GitHub MCP tools", "authenticated GitHub CLI (`gh`)", "current repository"} {
		if !strings.Contains(content.String, want) {
			t.Errorf("security content missing %q", want)
		}
	}

	var mcpContent string
	if err := db.QueryRow("SELECT content FROM guardrails WHERE name = 'mcp-tool-usage'").Scan(&mcpContent); err != nil {
		t.Fatalf("scan mcp-tool-usage: %v", err)
	}
	for _, want := range []string{"Prefer the GitHub MCP tools", "GitHub CLI (`gh`)", "Do not make remote-only code patches"} {
		if !strings.Contains(mcpContent, want) {
			t.Errorf("mcp-tool-usage content missing %q", want)
		}
	}

	var memoryContent string
	if err := db.QueryRow("SELECT content FROM guardrails WHERE name = 'memory-scope'").Scan(&memoryContent); err != nil {
		t.Fatalf("scan memory-scope: %v", err)
	}
	for _, want := range []string{"Existing memory:", "AI CLI", "current `(agent, repository)` pair", "Stay bound to the repository"} {
		if !strings.Contains(memoryContent, want) {
			t.Errorf("memory-scope content missing %q", want)
		}
	}
}

// TestGuardrailsCRUD exercises every store-side guardrail operation:
// the seeded built-in row is visible, operator rows can be created and
// updated, the render-order query returns enabled rows in the right
// order, deleting works and propagates *ErrNotFound, and reset copies
// default_content back into content for built-ins while rejecting reset
// on operator rows that have no default to fall back to.
func TestGuardrailsCRUD(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// 1. Seeded built-in rows are visible via every read path. Today there
	//    are four: `security` (position 0, migration 010),
	//    `discretion` (position 5, migration 013), `memory-scope`
	//    (position 7, migration 016), and `mcp-tool-usage`
	//    (position 10, migration 012). All four must be flagged
	//    is_builtin and have default_content == content on a fresh
	//    install.
	all, err := store.ReadAllGuardrails(db)
	if err != nil {
		t.Fatalf("ReadAllGuardrails: %v", err)
	}
	if len(all) != 4 || all[0].Name != "security" || all[1].Name != "discretion" || all[2].Name != "memory-scope" || all[3].Name != "mcp-tool-usage" {
		t.Fatalf("seed: got %v, want [security discretion memory-scope mcp-tool-usage]", names(all))
	}
	for _, g := range all {
		if !g.IsBuiltin || !g.Enabled {
			t.Errorf("%q: builtin=%v enabled=%v", g.Name, g.IsBuiltin, g.Enabled)
		}
		if g.DefaultContent == "" || g.DefaultContent != g.Content {
			t.Errorf("%q: default_content must equal content on first migration", g.Name)
		}
	}
	if all[0].Position != 0 || all[1].Position != 5 || all[2].Position != 7 || all[3].Position != 10 {
		t.Errorf("positions: security=%d discretion=%d memory-scope=%d mcp-tool-usage=%d, want 0, 5, 7, 10",
			all[0].Position, all[1].Position, all[2].Position, all[3].Position)
	}

	// 2. Operator can add a custom guardrail; it lands at the configured
	//    position and shows up in render order after the security row.
	custom := fleet.Guardrail{
		Name:        "Code Style",
		Description: "Project conventions",
		Content:     "Always run gofmt before submitting.",
		Enabled:     true,
		Position:    50,
	}
	if err := store.UpsertGuardrail(db, custom); err != nil {
		t.Fatalf("UpsertGuardrail (insert): %v", err)
	}
	enabled, err := store.ReadEnabledGuardrails(db)
	if err != nil {
		t.Fatalf("ReadEnabledGuardrails: %v", err)
	}
	if len(enabled) != 5 || enabled[0].Name != "security" || enabled[1].Name != "discretion" || enabled[2].Name != "memory-scope" || enabled[3].Name != "mcp-tool-usage" || enabled[4].Name != "code-style" {
		t.Errorf("render order: got %v, want [security discretion memory-scope mcp-tool-usage code-style]", names(enabled))
	}
	if !enabled[1].IsBuiltin {
		t.Error("discretion row should be flagged as built-in")
	}
	if !enabled[2].IsBuiltin {
		t.Error("memory-scope row should be flagged as built-in")
	}
	if !enabled[3].IsBuiltin {
		t.Error("mcp-tool-usage row should be flagged as built-in")
	}
	if enabled[4].IsBuiltin {
		t.Error("operator row should not be flagged as built-in")
	}

	// 3. Update through Upsert preserves built-in flag and default_content.
	editedSecurity := all[0]
	editedSecurity.Content = "edited by operator"
	if err := store.UpsertGuardrail(db, editedSecurity); err != nil {
		t.Fatalf("UpsertGuardrail (update security): %v", err)
	}
	got, err := store.GetGuardrail(db, "security")
	if err != nil {
		t.Fatalf("GetGuardrail: %v", err)
	}
	if got.Content != "edited by operator" {
		t.Errorf("security content after edit: got %q, want %q", got.Content, "edited by operator")
	}
	if !got.IsBuiltin {
		t.Error("UpsertGuardrail must not clear is_builtin on built-in row")
	}
	if got.DefaultContent == got.Content {
		t.Error("UpsertGuardrail must not overwrite default_content on built-in row")
	}

	// 4. ResetGuardrail copies default_content back into content for built-ins.
	if err := store.ResetGuardrail(db, "security"); err != nil {
		t.Fatalf("ResetGuardrail: %v", err)
	}
	got, err = store.GetGuardrail(db, "security")
	if err != nil {
		t.Fatalf("GetGuardrail after reset: %v", err)
	}
	if got.Content != got.DefaultContent {
		t.Error("ResetGuardrail did not restore default_content into content")
	}

	// 5. ResetGuardrail rejects reset on operator rows with no default.
	err = store.ResetGuardrail(db, "code-style")
	var valErr *store.ErrValidation
	if !errors.As(err, &valErr) {
		t.Errorf("reset operator row: want *ErrValidation, got %T: %v", err, err)
	}

	// 6. Delete removes the row; second delete returns *ErrNotFound.
	if err := store.DeleteGuardrail(db, "code-style"); err != nil {
		t.Fatalf("DeleteGuardrail: %v", err)
	}
	err = store.DeleteGuardrail(db, "code-style")
	var notFound *store.ErrNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("delete missing: want *ErrNotFound, got %T: %v", err, err)
	}
	all, err = store.ReadAllGuardrails(db)
	if err != nil {
		t.Fatalf("ReadAllGuardrails after delete: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("after delete: got %d rows, want 4 (security + discretion + memory-scope + mcp-tool-usage built-ins)", len(all))
	}
}

func names(gs []fleet.Guardrail) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Name
	}
	return out
}

// TestImportLoadGuardrails covers the YAML round-trip path: an import that
// carries operator-added guardrails is persisted alongside the seeded
// built-in 'security' row, the load path returns every row (built-ins +
// operator) ready for a subsequent /export, and re-importing an edited
// security guardrail updates its content but leaves is_builtin and
// default_content untouched (the migration is the sole source for those).
func TestImportLoadGuardrails(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// 1. Import operator-added rows + an edited security override.
	cfg := minimalCfg()
	cfg.Guardrails = []fleet.Guardrail{
		{Name: "security", Content: "Operator-edited security body.", Enabled: true, Position: 0},
		{Name: "code-style", Description: "Conventions", Content: "Always run gofmt.", Enabled: true, Position: 50},
	}
	if err := store.Import(db, cfg); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// 2. Load returns every row, ordered for the renderer.
	out, err := store.Load(db)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := names(out.Guardrails); len(got) != 5 || got[0] != "security" || got[1] != "discretion" || got[2] != "memory-scope" || got[3] != "mcp-tool-usage" || got[4] != "code-style" {
		t.Fatalf("Load order: got %v, want [security discretion memory-scope mcp-tool-usage code-style]", got)
	}
	sec := out.Guardrails[0]
	if !sec.IsBuiltin {
		t.Error("built-in security row must keep IsBuiltin = true after import")
	}
	if sec.Content != "Operator-edited security body." {
		t.Errorf("security content: got %q", sec.Content)
	}
	if sec.DefaultContent == "" || sec.DefaultContent == sec.Content {
		t.Error("DefaultContent must remain the migration's seeded text after operator override")
	}
	dis := out.Guardrails[1]
	if !dis.IsBuiltin || dis.DefaultContent == "" {
		t.Errorf("discretion must be built-in with non-empty DefaultContent; got builtin=%v default-len=%d",
			dis.IsBuiltin, len(dis.DefaultContent))
	}
	memScope := out.Guardrails[2]
	if !memScope.IsBuiltin || memScope.DefaultContent == "" {
		t.Errorf("memory-scope must be built-in with non-empty DefaultContent; got builtin=%v default-len=%d",
			memScope.IsBuiltin, len(memScope.DefaultContent))
	}
	mcp := out.Guardrails[3]
	if !mcp.IsBuiltin || mcp.DefaultContent == "" {
		t.Errorf("mcp-tool-usage must be built-in with non-empty DefaultContent; got builtin=%v default-len=%d",
			mcp.IsBuiltin, len(mcp.DefaultContent))
	}
	custom := out.Guardrails[4]
	if custom.IsBuiltin || custom.DefaultContent != "" {
		t.Errorf("operator row must not be built-in and must have empty DefaultContent; got builtin=%v default=%q",
			custom.IsBuiltin, custom.DefaultContent)
	}
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

	// Second open should not fail, migrations already applied.
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

	// Daemon runtime config is process-owned and is no longer persisted in the
	// DB. Store.Load only materialises fleet entities; FinishLoad later applies
	// daemon defaults/env overrides for the process.
	if out.Daemon.Log.Level != "" || out.Daemon.HTTP.ListenAddr != "" || out.Daemon.Processor.Dispatch.MaxDepth != 0 {
		t.Errorf("daemon runtime config should not be loaded from DB: %+v", out.Daemon)
	}

	// Backends are fleet entities and still round-trip through SQLite.
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
	idx := slices.IndexFunc(out.Agents, func(a fleet.Agent) bool { return a.Name == "coder" })
	if idx < 0 {
		t.Fatal("coder agent not found after load")
	}
	coder := out.Agents[idx]
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

// TestLoadEmptyDatabase verifies that Load on a fresh (empty) database
// returns a zero-value *Config without error. The daemon's startup path
// runs config.FinishLoad on the result, which fills every required
// field with built-in defaults, so an empty store boots cleanly with
// no YAML import required.
func TestLoadEmptyDatabase(t *testing.T) {
	t.Setenv("AGENTS_HTTP_LISTEN_ADDR", "127.0.0.1:9090")
	t.Setenv("AGENTS_PROCESSOR_MAX_CONCURRENT_AGENTS", "7")
	db := openTestDB(t)

	cfg, err := store.Load(db)
	if err != nil {
		t.Fatalf("Load on empty database should succeed, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load on empty database returned nil config")
	}
	// Daemon block left at zero, applyDefaults populates it later.
	if cfg.Daemon.HTTP.ListenAddr != "" {
		t.Errorf("expected zero daemon block before FinishLoad; got listen_addr=%q", cfg.Daemon.HTTP.ListenAddr)
	}

	// FinishLoad turns the zero block into a fully-populated config the daemon
	// can boot against, with startup env overrides applied on top of defaults.
	cfg, err = config.FinishLoad(cfg)
	if err != nil {
		t.Fatalf("FinishLoad should fill defaults on empty config: %v", err)
	}
	if cfg.Daemon.HTTP.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("FinishLoad listen_addr = %q, want env override on empty DB", cfg.Daemon.HTTP.ListenAddr)
	}
	if cfg.Daemon.Log.Level == "" {
		t.Error("FinishLoad did not populate log level from defaults")
	}
	if cfg.Daemon.HTTP.WebhookSecretEnv == "" {
		t.Error("FinishLoad did not populate webhook secret env from defaults")
	}
	if cfg.Daemon.Processor.MaxConcurrentAgents != 7 {
		t.Errorf("FinishLoad max_concurrent_agents = %d, want env override on empty DB", cfg.Daemon.Processor.MaxConcurrentAgents)
	}
}

func seedAgent(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if err := store.UpsertBackend(db, "claude", fleet.Backend{
		Command: "claude",
	}); err != nil {
		t.Fatalf("seedAgent backend: %v", err)
	}
	if err := store.UpsertAgent(db, fleet.Agent{
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
