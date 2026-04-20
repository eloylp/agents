package store_test

import (
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// ──── Agents ─────────────────────────────────────────────────────────────────

func TestUpsertAndReadAgents(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

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
	if len(agents) != 1 {
		t.Fatalf("ReadAgents: got %d agents, want 1", len(agents))
	}
	got := agents[0]
	if got.Name != "coder" {
		t.Errorf("Name: got %q, want %q", got.Name, "coder")
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

	a := config.AgentDef{Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{}}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if err := store.DeleteAgent(db, "coder"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("got %d agents after delete, want 0", len(agents))
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

func TestDeleteBackend(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

	b := config.AIBackendConfig{Command: "claude", Args: []string{}, Env: map[string]string{}}
	if err := store.UpsertBackend(db, "claude", b); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}
	if err := store.DeleteBackend(db, "claude"); err != nil {
		t.Fatalf("DeleteBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	if len(backends) != 0 {
		t.Errorf("got %d backends after delete, want 0", len(backends))
	}
}

// ──── Repos ──────────────────────────────────────────────────────────────────

func TestUpsertAndReadRepos(t *testing.T) {
	t.Parallel()
	db, cleanup := openTestDB(t)
	defer cleanup()

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

	if err := store.UpsertAgent(db, config.AgentDef{
		Name: "coder", Backend: "claude", Prompt: "p", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	r := config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
	}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}
	if err := store.DeleteRepo(db, "owner/repo"); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("got %d repos after delete, want 0", len(repos))
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
