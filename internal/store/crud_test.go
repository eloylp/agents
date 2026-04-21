package store_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// seedBackend inserts a minimal backend into db so that agent upserts that
// reference it pass cross-ref validation.
func seedBackend(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	b := config.AIBackendConfig{Command: name, Args: []string{}, Env: map[string]string{}}
	if err := store.UpsertBackend(db, name, b); err != nil {
		t.Fatalf("seedBackend %s: %v", name, err)
	}
}

// seedSkill inserts a minimal skill into db so that agent upserts that
// reference it pass cross-ref validation.
func seedSkill(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if err := store.UpsertSkill(db, name, config.SkillDef{Prompt: "skill prompt"}); err != nil {
		t.Fatalf("seedSkill %s: %v", name, err)
	}
}

// ──── Agents ─────────────────────────────────────────────────────────────────

func TestUpsertAndReadAgents(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	seedSkill(t, db, "architect")

	// "pr-reviewer" must exist (with a description) before "coder" can list it
	// in can_dispatch.
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:        "pr-reviewer",
		Backend:     "claude",
		Prompt:      "review code",
		Description: "A code review agent",
		Skills:      []string{},
		CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent pr-reviewer: %v", err)
	}

	a := config.AgentDef{
		Name:          "coder",
		Backend:       "claude",
		Skills:        []string{"architect"},
		Prompt:        "You write code.",
		AllowPRs:      true,
		AllowDispatch: true,
		CanDispatch:   []string{"pr-reviewer"},
		Description:   "A coding agent",
	}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	// 2 agents: pr-reviewer (seeded for can_dispatch) + coder.
	var got *config.AgentDef
	for i := range agents {
		if agents[i].Name == "coder" {
			got = &agents[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("ReadAgents: coder not found in %v", agents)
	}
	if !got.AllowPRs {
		t.Error("AllowPRs: want true")
	}
	if len(got.CanDispatch) != 1 || got.CanDispatch[0] != "pr-reviewer" {
		t.Errorf("CanDispatch: got %v", got.CanDispatch)
	}
}

func TestUpsertAgentIsIdempotent(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	a := config.AgentDef{Name: "coder", Backend: "claude", Prompt: "v1", Skills: []string{}, CanDispatch: []string{}}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	a.Prompt = "v2"
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].Prompt != "v2" {
		t.Errorf("Prompt: got %q, want %q", agents[0].Prompt, "v2")
	}
}

func TestDeleteAgent(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	// Seed two agents so that deleting one still leaves the system valid.
	for _, name := range []string{"coder", "reviewer"} {
		if err := store.UpsertAgent(db, config.AgentDef{
			Name: name, Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
		}); err != nil {
			t.Fatalf("UpsertAgent %s: %v", name, err)
		}
	}
	if err := store.DeleteAgent(db, "coder"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("got %d agents after delete, want 1", len(agents))
	}
	if agents[0].Name != "reviewer" {
		t.Errorf("remaining agent: got %q, want %q", agents[0].Name, "reviewer")
	}
}

func TestDeleteAgentNonExistentIsNoError(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.DeleteAgent(db, "ghost"); err != nil {
		t.Errorf("DeleteAgent non-existent: %v", err)
	}
}

// ──── Skills ─────────────────────────────────────────────────────────────────

func TestUpsertAndReadSkills(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	s := config.SkillDef{Prompt: "Focus on architecture."}
	if err := store.UpsertSkill(db, "architect", s); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills["architect"].Prompt != "Focus on architecture." {
		t.Errorf("Prompt: got %q", skills["architect"].Prompt)
	}
}

func TestDeleteSkill(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.UpsertSkill(db, "architect", config.SkillDef{Prompt: "p"}); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	if err := store.DeleteSkill(db, "architect"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("got %d skills after delete, want 0", len(skills))
	}
}

// ──── Backends ───────────────────────────────────────────────────────────────

func TestUpsertAndReadBackends(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	b := config.AIBackendConfig{
		Command:        "claude",
		Args:           []string{"-p", "--output-format", "json"},
		Env:            map[string]string{"K": "V"},
		TimeoutSeconds: 300,
		MaxPromptChars: 8000,
	}
	if err := store.UpsertBackend(db, "claude", b); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	if len(backends) != 1 {
		t.Fatalf("got %d backends, want 1", len(backends))
	}
	got := backends["claude"]
	if got.Command != "claude" {
		t.Errorf("Command: got %q, want %q", got.Command, "claude")
	}
	if len(got.Args) != 3 {
		t.Errorf("Args: got %v", got.Args)
	}
	if got.Env["K"] != "V" {
		t.Errorf("Env[K]: got %q, want %q", got.Env["K"], "V")
	}
}

func TestUpsertBackendAppliesDefaults(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Persist a backend with zero numeric fields — the same payload that
	// POST /api/store/backends would send when omitting timeout_seconds and
	// max_prompt_chars from the request body.
	b := config.AIBackendConfig{Command: "claude", Args: []string{}, Env: map[string]string{}}
	if err := store.UpsertBackend(db, "claude", b); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	got := backends["claude"]
	// startup-equivalent defaults must have been applied before storage.
	if got.TimeoutSeconds != 600 {
		t.Errorf("TimeoutSeconds: got %d, want 600 (startup default)", got.TimeoutSeconds)
	}
	if got.MaxPromptChars != 12000 {
		t.Errorf("MaxPromptChars: got %d, want 12000 (startup default)", got.MaxPromptChars)
	}
}

func TestDeleteBackend(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Seed two backends so that deleting one still leaves the system valid.
	for _, name := range []string{"claude", "codex"} {
		if err := store.UpsertBackend(db, name, config.AIBackendConfig{
			Command: name, Args: []string{}, Env: map[string]string{},
		}); err != nil {
			t.Fatalf("UpsertBackend %s: %v", name, err)
		}
	}
	if err := store.DeleteBackend(db, "claude"); err != nil {
		t.Fatalf("DeleteBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	if len(backends) != 1 {
		t.Errorf("got %d backends after delete, want 1", len(backends))
	}
	if _, ok := backends["codex"]; !ok {
		t.Error("codex backend should remain after deleting claude")
	}
}

// ──── Repos ──────────────────────────────────────────────────────────────────

func TestUpsertAndReadRepos(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	// UpsertRepo requires the agents referenced by bindings to exist.
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	enabled := true
	r := config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use: []config.Binding{
			{Agent: "coder", Labels: []string{"ai:fix"}, Enabled: &enabled},
		},
	}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}
	got := repos[0]
	if got.Name != "owner/repo" {
		t.Errorf("Name: got %q", got.Name)
	}
	if !got.Enabled {
		t.Error("Enabled: want true")
	}
	if len(got.Use) != 1 {
		t.Fatalf("bindings: got %d, want 1", len(got.Use))
	}
	if got.Use[0].Agent != "coder" {
		t.Errorf("binding agent: got %q", got.Use[0].Agent)
	}
	if len(got.Use[0].Labels) != 1 || got.Use[0].Labels[0] != "ai:fix" {
		t.Errorf("binding labels: got %v", got.Use[0].Labels)
	}
}

func TestUpsertRepoReplacesBindings(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	r := config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use: []config.Binding{
			{Agent: "coder", Labels: []string{"ai:fix"}},
			{Agent: "coder", Cron: "0 9 * * *"},
		},
	}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Re-upsert with only one binding.
	r.Use = []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos[0].Use) != 1 {
		t.Errorf("bindings after re-upsert: got %d, want 1", len(repos[0].Use))
	}
}

func TestDeleteRepo(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Seed two repos so that deleting one still leaves at least one enabled.
	for _, name := range []string{"owner/repo", "owner/other"} {
		if err := store.UpsertRepo(db, config.RepoDef{
			Name:    name,
			Enabled: true,
			Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
		}); err != nil {
			t.Fatalf("UpsertRepo %s: %v", name, err)
		}
	}
	if err := store.DeleteRepo(db, "owner/repo"); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("got %d repos after delete, want 1", len(repos))
	}
	if repos[0].Name != "owner/other" {
		t.Errorf("remaining repo: got %q, want %q", repos[0].Name, "owner/other")
	}

	// Verify that bindings were also deleted (no orphan rows).
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM bindings WHERE repo='owner/repo'").Scan(&count); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if count != 0 {
		t.Errorf("orphan bindings after DeleteRepo: got %d, want 0", count)
	}
}

// TestReadSnapshot verifies that ReadSnapshot returns both agents and repos
// as a consistent point-in-time view.
func TestReadSnapshot(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	// Seed one agent and one repo.
	a := config.AgentDef{
		Name:    "coder",
		Backend: "claude",
		Skills:  []string{},
		Prompt:  "You write code.",
	}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	enabled := true
	r := config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "coder", Events: []string{"issues.labeled"}, Enabled: &enabled}},
	}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	agents, repos, skills, backends, err := store.ReadSnapshot(db)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "coder" {
		t.Errorf("agents: want [{coder}], got %v", agents)
	}
	if len(repos) != 1 || repos[0].Name != "owner/repo" {
		t.Errorf("repos: want [{owner/repo}], got %v", repos)
	}
	if len(repos[0].Use) != 1 || repos[0].Use[0].Agent != "coder" {
		t.Errorf("bindings: want 1 binding for coder, got %v", repos[0].Use)
	}
	if skills == nil {
		t.Error("skills: want non-nil map, got nil")
	}
	if backends == nil {
		t.Error("backends: want non-nil map, got nil")
	}
}

// ──── Cross-ref validation ────────────────────────────────────────────────────

func TestUpsertAgentCrossRefErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, db *sql.DB)
		agent   config.AgentDef
		wantErr string
	}{
		{
			name:    "unknown backend",
			setup:   func(t *testing.T, db *sql.DB) { t.Helper() }, // no backend seeded
			agent:   config.AgentDef{Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}},
			wantErr: "unknown backend",
		},
		{
			name:    "unknown skill",
			setup:   func(t *testing.T, db *sql.DB) { seedBackend(t, db, "claude") },
			agent:   config.AgentDef{Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{"architect"}},
			wantErr: "unknown skill",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, cleanup := openTestDB(t)
			defer cleanup()
			tc.setup(t, db)
			err := store.UpsertAgent(db, tc.agent)
			if err == nil {
				t.Fatalf("UpsertAgent with %s: want error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("unexpected error message: %v", err)
			}
		})
	}
}

func TestUpsertRepoRejectedWithUnknownAgent(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// No agent seeded — binding references "ghost". The FK constraint on
	// bindings.agent may fire first, or validateCrossRefs catches it; either
	// way an error must be returned and nothing must be committed.
	err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "ghost", Labels: []string{"ai:fix"}}},
	})
	if err == nil {
		t.Fatal("UpsertRepo with unknown agent binding: want error, got nil")
	}

	// Verify the repo was not committed.
	repos, readErr := store.ReadRepos(db)
	if readErr != nil {
		t.Fatalf("ReadRepos: %v", readErr)
	}
	if len(repos) != 0 {
		t.Errorf("repo was committed despite invalid binding: %v", repos)
	}
}

func TestDeleteBackendRejectedWhenAgentReferences(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Seed two backends so that the "at least one backend" constraint is not the
	// reason the delete fails — only the agent reference should block it.
	seedBackend(t, db, "claude")
	seedBackend(t, db, "codex")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:    "coder",
		Backend: "claude",
		Prompt:  "p",
		Skills:  []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Deleting "claude" while "coder" references it must fail (codex remains).
	err := store.DeleteBackend(db, "claude")
	if err == nil {
		t.Fatal("DeleteBackend still referenced by agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Backend must still be present.
	backends, readErr := store.ReadBackends(db)
	if readErr != nil {
		t.Fatalf("ReadBackends: %v", readErr)
	}
	if _, ok := backends["claude"]; !ok {
		t.Error("backend was deleted despite being still referenced")
	}
}

func TestDeleteSkillRejectedWhenAgentReferences(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	seedSkill(t, db, "architect")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:    "coder",
		Backend: "claude",
		Prompt:  "p",
		Skills:  []string{"architect"},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Deleting "architect" while "coder" references it must fail.
	err := store.DeleteSkill(db, "architect")
	if err == nil {
		t.Fatal("DeleteSkill still referenced by agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Skill must still be present.
	skills, readErr := store.ReadSkills(db)
	if readErr != nil {
		t.Fatalf("ReadSkills: %v", readErr)
	}
	if _, ok := skills["architect"]; !ok {
		t.Error("skill was deleted despite being still referenced")
	}
}

func TestDeleteAgentRejectedWhenDispatchListReferences(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")

	// Seed two agents: "dispatcher" can_dispatch to "target".
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:        "target",
		Backend:     "claude",
		Prompt:      "p",
		Description: "a dispatchable target",
		Skills:      []string{},
		CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent target: %v", err)
	}
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:        "dispatcher",
		Backend:     "claude",
		Prompt:      "p",
		Skills:      []string{},
		CanDispatch: []string{"target"},
	}); err != nil {
		t.Fatalf("UpsertAgent dispatcher: %v", err)
	}

	// Deleting "target" while "dispatcher" lists it in can_dispatch must fail.
	err := store.DeleteAgent(db, "target")
	if err == nil {
		t.Fatal("DeleteAgent still in can_dispatch list: want error, got nil")
	}
	if !strings.Contains(err.Error(), "can_dispatch") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ──── Field-level validation tests ───────────────────────────────────────────

func TestUpsertBackendValidationErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		bName   string
		cfg     config.AIBackendConfig
		wantErr string
	}{
		{
			name:    "empty command",
			bName:   "claude",
			cfg:     config.AIBackendConfig{Command: "", Args: []string{}, Env: map[string]string{}},
			wantErr: "command is required",
		},
		{
			name:    "invalid name",
			bName:   "unknown-ai",
			cfg:     config.AIBackendConfig{Command: "ai", Args: []string{}, Env: map[string]string{}},
			wantErr: "unsupported ai backend",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, cleanup := openTestDB(t)
			defer cleanup()
			err := store.UpsertBackend(db, tc.bName, tc.cfg)
			if err == nil {
				t.Fatalf("UpsertBackend with %s: want error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpsertSkillRejectedWithEmptyPrompt(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	err := store.UpsertSkill(db, "testing", config.SkillDef{Prompt: ""})
	if err == nil {
		t.Fatal("UpsertSkill with empty prompt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpsertAgentRejectedWithEmptyPrompt(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	err := store.UpsertAgent(db, config.AgentDef{
		Name:    "coder",
		Backend: "claude",
		Prompt:  "",
		Skills:  []string{},
	})
	if err == nil {
		t.Fatal("UpsertAgent with empty prompt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpsertRepoRejectedWithNoTrigger(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	// Binding has no labels, events, or cron — invalid.
	err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "coder"}},
	})
	if err == nil {
		t.Fatal("UpsertRepo with no-trigger binding: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no trigger") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpsertRepoRejectedWithMixedTriggers(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	// Binding mixes labels and events — invalid.
	err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use: []config.Binding{{
			Agent:  "coder",
			Labels: []string{"ai:fix"},
			Events: []string{"push"},
		}},
	})
	if err == nil {
		t.Fatal("UpsertRepo with mixed-trigger binding: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mixes multiple trigger types") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeleteAgentRejectedAsLast(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	err := store.DeleteAgent(db, "coder")
	if err == nil {
		t.Fatal("DeleteAgent last agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one agent") {
		t.Errorf("unexpected error: %v", err)
	}

	// Agent must still be present.
	agents, readErr := store.ReadAgents(db)
	if readErr != nil {
		t.Fatalf("ReadAgents: %v", readErr)
	}
	if len(agents) != 1 {
		t.Errorf("agent count after rejected delete: got %d, want 1", len(agents))
	}
}

func TestDeleteBackendRejectedAsLast(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	if err := store.UpsertBackend(db, "claude", config.AIBackendConfig{
		Command: "claude", Args: []string{}, Env: map[string]string{},
	}); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}

	err := store.DeleteBackend(db, "claude")
	if err == nil {
		t.Fatal("DeleteBackend last backend: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one ai_backends") {
		t.Errorf("unexpected error: %v", err)
	}

	// Backend must still be present.
	backends, readErr := store.ReadBackends(db)
	if readErr != nil {
		t.Fatalf("ReadBackends: %v", readErr)
	}
	if _, ok := backends["claude"]; !ok {
		t.Error("backend was deleted despite being the last one")
	}
}

func TestUpsertRepoRejectedWhenDisablingLastEnabled(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo (setup): %v", err)
	}

	// Upserting the same repo with enabled=false should be rejected because it
	// would leave no enabled repos — matching the invariant enforced by DeleteRepo.
	err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: false,
		Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
	})
	if err == nil {
		t.Fatal("UpsertRepo disabling last enabled repo: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one repo must be enabled") {
		t.Errorf("unexpected error: %v", err)
	}

	// The original enabled repo must still be present and enabled.
	repos, readErr := store.ReadRepos(db)
	if readErr != nil {
		t.Fatalf("ReadRepos: %v", readErr)
	}
	if len(repos) != 1 {
		t.Errorf("repo count after rejected upsert: got %d, want 1", len(repos))
	}
	if !repos[0].Enabled {
		t.Error("repo was disabled despite rejected upsert")
	}
}

func TestDeleteRepoRejectedAsLastEnabled(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	err := store.DeleteRepo(db, "owner/repo")
	if err == nil {
		t.Fatal("DeleteRepo last enabled repo: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one repo must be enabled") {
		t.Errorf("unexpected error: %v", err)
	}

	// Repo must still be present.
	repos, readErr := store.ReadRepos(db)
	if readErr != nil {
		t.Fatalf("ReadRepos: %v", readErr)
	}
	if len(repos) != 1 {
		t.Errorf("repo count after rejected delete: got %d, want 1", len(repos))
	}
}

// ──── Normalization ───────────────────────────────────────────────────────────

// TestUpsertNormalizesNames verifies that UpsertAgent, UpsertSkill,
// UpsertBackend, and UpsertRepo all lowercase+trim entity keys before writing
// to SQLite. This ensures the stored form matches what FinishLoad would
// produce at startup, so AgentByName lookups and registerJobs cron bindings
// never silently diverge from the persisted rows after a live CRUD write.
func TestUpsertNormalizesNames(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Backend — mixed-case name should be stored lowercase.
	if err := store.UpsertBackend(db, "Claude", config.AIBackendConfig{
		Command: "claude", Args: []string{}, Env: map[string]string{},
	}); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	if _, ok := backends["claude"]; !ok {
		t.Errorf("backend name not normalised: got keys %v, want 'claude'", mapKeys(backends))
	}
	if _, bad := backends["Claude"]; bad {
		t.Error("original mixed-case key 'Claude' should not be present after normalisation")
	}

	// Skill — mixed-case key should be stored lowercase.
	if err := store.UpsertSkill(db, "Architect", config.SkillDef{Prompt: "p"}); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if _, ok := skills["architect"]; !ok {
		t.Errorf("skill name not normalised: got keys %v, want 'architect'", mapKeys(skills))
	}

	// Agent — mixed-case name, backend, and skill reference should be stored lowercase.
	if err := store.UpsertAgent(db, config.AgentDef{
		Name:        "Coder",
		Backend:     "Claude",
		Prompt:      "p",
		Skills:      []string{"Architect"},
		CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("ReadAgents: expected at least one agent")
	}
	got := agents[0]
	if got.Name != "coder" {
		t.Errorf("agent name not normalised: got %q, want 'coder'", got.Name)
	}
	if got.Backend != "claude" {
		t.Errorf("agent backend not normalised: got %q, want 'claude'", got.Backend)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "architect" {
		t.Errorf("agent skills not normalised: got %v, want ['architect']", got.Skills)
	}

	// Repo — binding agent name should be stored lowercase.
	if err := store.UpsertRepo(db, config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "Coder", Labels: []string{"ai:fix"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) == 0 || len(repos[0].Use) == 0 {
		t.Fatal("ReadRepos: expected repo with at least one binding")
	}
	if repos[0].Use[0].Agent != "coder" {
		t.Errorf("binding agent not normalised: got %q, want 'coder'", repos[0].Use[0].Agent)
	}
}

// TestUpsertSkillNormalizesPrompt verifies that UpsertSkill trims Prompt and
// PromptFile before persisting, matching the normalization startup applies.
// A whitespace-only prompt must be trimmed to "" and then rejected by
// validation — otherwise the write API would persist state that the daemon
// refuses to load on restart.
func TestUpsertSkillNormalizesPrompt(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Whitespace-only prompt should be trimmed to "" and rejected.
	err := store.UpsertSkill(db, "testing", config.SkillDef{Prompt: "   "})
	if err == nil {
		t.Fatal("UpsertSkill with whitespace-only prompt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("unexpected error message: %v", err)
	}

	// A prompt with surrounding whitespace should be trimmed and stored cleanly.
	if err := store.UpsertSkill(db, "testing", config.SkillDef{Prompt: "  skill guidance  "}); err != nil {
		t.Fatalf("UpsertSkill with padded prompt: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if got := skills["testing"].Prompt; got != "skill guidance" {
		t.Errorf("Prompt not trimmed: got %q, want %q", got, "skill guidance")
	}
}

// TestUpsertBackendNormalizesCommandAndEnv verifies that UpsertBackend trims
// Command and removes blank env keys before persisting, matching the
// normalization startup applies in normalize(). This prevents a write that
// passes validation from creating a backend that the daemon refuses to load
// on restart after startup normalization changes its shape.
func TestUpsertBackendNormalizesCommandAndEnv(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	// Whitespace-only command must be trimmed to "" and rejected.
	err := store.UpsertBackend(db, "claude", config.AIBackendConfig{
		Command: "   ",
		Env:     map[string]string{},
	})
	if err == nil {
		t.Fatal("UpsertBackend with whitespace-only command: want error, got nil")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("unexpected error: %v", err)
	}

	// Padded command should be stored trimmed; blank env key should be dropped.
	if err := store.UpsertBackend(db, "claude", config.AIBackendConfig{
		Command: "  claude  ",
		Env:     map[string]string{"VALID_KEY": "val", "  ": "blank-key"},
	}); err != nil {
		t.Fatalf("UpsertBackend with padded command: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	got := backends["claude"]
	if got.Command != "claude" {
		t.Errorf("Command not trimmed: got %q, want %q", got.Command, "claude")
	}
	if _, ok := got.Env[""]; ok {
		t.Error("blank env key should have been removed")
	}
	if _, ok := got.Env["  "]; ok {
		t.Error("whitespace env key should have been removed")
	}
	if got.Env["VALID_KEY"] != "val" {
		t.Errorf("valid env key lost: got %v", got.Env)
	}
}

// mapKeys returns the keys from any map[string]T.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
