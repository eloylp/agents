package store_test

import (
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// seedBackend inserts a minimal backend into db so that agent upserts that
// reference it pass cross-ref validation.
func seedBackend(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	b := fleet.Backend{Command: name}
	if err := store.UpsertBackend(db, name, b); err != nil {
		t.Fatalf("seedBackend %s: %v", name, err)
	}
	if _, err := store.UpsertPrompt(db, fleet.Prompt{Name: "coder", Content: "test prompt"}); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
}

// seedSkill inserts a minimal skill into db so that agent upserts that
// reference it pass cross-ref validation.
func seedSkill(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if err := store.UpsertSkill(db, name, fleet.Skill{Prompt: "skill prompt"}); err != nil {
		t.Fatalf("seedSkill %s: %v", name, err)
	}
}

// ──── Agents ─────────────────────────────────────────────────────────────────

func TestUpsertAndReadAgents(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	seedSkill(t, db, "architect")

	// "pr-reviewer" must exist (with a description) before "coder" can list it
	// in can_dispatch.
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:          "pr-reviewer",
		Backend:       "claude",
		PromptRef:     "coder",
		Description:   "A code review agent",
		AllowDispatch: true,
		Skills:        []string{},
		CanDispatch:   []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent pr-reviewer: %v", err)
	}

	a := fleet.Agent{
		Name:          "coder",
		Backend:       "claude",
		Skills:        []string{"architect"},
		PromptRef:     "coder",
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
	var got *fleet.Agent
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	a := fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{}, CanDispatch: []string{}}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	a.PromptRef = "coder"
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
	if agents[0].PromptRef != "coder" {
		t.Errorf("PromptRef: got %q, want coder", agents[0].PromptRef)
	}
	if agents[0].ID == "" {
		t.Fatal("ID is empty, want stable generated id")
	}
	firstID := agents[0].ID

	a.PromptRef = "coder"
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	agents, err = store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents after third upsert: %v", err)
	}
	if agents[0].ID != firstID {
		t.Errorf("ID changed across upsert: got %q, want %q", agents[0].ID, firstID)
	}
}

func TestAgentsAndReposAreUniquePerWorkspace(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedBackend(t, db, "claude")
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}

	defaultAgent := fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "Default coder"}
	teamAgent := fleet.Agent{WorkspaceID: "team-a", Name: "coder", Backend: "claude", PromptRef: "coder", Description: "Team coder"}
	if err := store.UpsertAgent(db, defaultAgent); err != nil {
		t.Fatalf("UpsertAgent default: %v", err)
	}
	if err := store.UpsertAgent(db, teamAgent); err != nil {
		t.Fatalf("UpsertAgent team: %v", err)
	}

	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:default"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo default: %v", err)
	}
	if err := store.UpsertRepo(db, fleet.Repo{
		WorkspaceID: "team-a",
		Name:        "owner/repo",
		Enabled:     true,
		Use:         []fleet.Binding{{Agent: "coder", Labels: []string{"ai:team"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo team: %v", err)
	}

	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	var defaultID, teamID string
	for _, a := range agents {
		if a.Name != "coder" {
			continue
		}
		switch a.WorkspaceID {
		case fleet.DefaultWorkspaceID:
			defaultID = a.ID
		case "team-a":
			teamID = a.ID
		}
	}
	if defaultID == "" || teamID == "" || defaultID == teamID {
		t.Fatalf("agent ids: default=%q team=%q, want distinct non-empty ids", defaultID, teamID)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	labelsByWorkspace := map[string]string{}
	for _, r := range repos {
		if r.Name == "owner/repo" && len(r.Use) == 1 && len(r.Use[0].Labels) == 1 {
			labelsByWorkspace[r.WorkspaceID] = r.Use[0].Labels[0]
		}
	}
	if labelsByWorkspace[fleet.DefaultWorkspaceID] != "ai:default" || labelsByWorkspace["team-a"] != "ai:team" {
		t.Fatalf("repo bindings by workspace = %+v, want isolated default/team bindings", labelsByWorkspace)
	}
}

func TestUpsertAgentIgnoresCallerProvidedID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	a := fleet.Agent{ID: "client-id", Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{}, CanDispatch: []string{}}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].ID == "" || agents[0].ID == "client-id" {
		t.Fatalf("ID = %q, want server-generated id", agents[0].ID)
	}
	firstID := agents[0].ID

	a.ID = "mutated-client-id"
	a.PromptRef = "coder"
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent update: %v", err)
	}
	agents, err = store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents after update: %v", err)
	}
	if agents[0].ID != firstID {
		t.Fatalf("ID after update = %q, want preserved server id %q", agents[0].ID, firstID)
	}
}

func TestGraphLayoutPersistsByStableAgentID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{}, CanDispatch: []string{}}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	id := agents[0].ID
	if id == "" {
		t.Fatal("agent ID is empty")
	}

	if err := store.UpsertGraphLayout(db, []store.GraphNodePosition{{NodeID: id, X: 12.5, Y: -8}}); err != nil {
		t.Fatalf("UpsertGraphLayout: %v", err)
	}
	got, err := store.ReadGraphLayout(db)
	if err != nil {
		t.Fatalf("ReadGraphLayout: %v", err)
	}
	if len(got) != 1 || got[0].NodeID != id || got[0].X != 12.5 || got[0].Y != -8 {
		t.Fatalf("layout = %+v, want one position for %s", got, id)
	}

	if err := store.UpsertAgent(db, fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{}, CanDispatch: []string{}}); err != nil {
		t.Fatalf("UpsertAgent rename-preserve simulation: %v", err)
	}
	got, err = store.ReadGraphLayout(db)
	if err != nil {
		t.Fatalf("ReadGraphLayout after agent update: %v", err)
	}
	if len(got) != 1 || got[0].NodeID != id {
		t.Fatalf("layout after agent update = %+v, want node id %s preserved", got, id)
	}
}

func TestGraphLayoutIsWorkspaceScoped(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	for _, agent := range []fleet.Agent{
		{Name: "default-coder", Backend: "claude", PromptRef: "coder", Description: "default coder", WorkspaceID: fleet.DefaultWorkspaceID, Skills: []string{}, CanDispatch: []string{}},
		{Name: "team-coder", Backend: "claude", PromptRef: "coder", Description: "team coder", WorkspaceID: "team-a", Skills: []string{}, CanDispatch: []string{}},
	} {
		if err := store.UpsertAgent(db, agent); err != nil {
			t.Fatalf("UpsertAgent %s: %v", agent.Name, err)
		}
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	ids := map[string]string{}
	for _, a := range agents {
		ids[a.Name] = a.ID
	}

	if err := store.UpsertWorkspaceGraphLayout(db, fleet.DefaultWorkspaceID, []store.GraphNodePosition{{NodeID: ids["default-coder"], X: 1, Y: 2}}); err != nil {
		t.Fatalf("UpsertWorkspaceGraphLayout default: %v", err)
	}
	if err := store.UpsertWorkspaceGraphLayout(db, "team-a", []store.GraphNodePosition{{NodeID: ids["team-coder"], X: 3, Y: 4}}); err != nil {
		t.Fatalf("UpsertWorkspaceGraphLayout team-a: %v", err)
	}

	defaultLayout, err := store.ReadWorkspaceGraphLayout(db, fleet.DefaultWorkspaceID)
	if err != nil {
		t.Fatalf("ReadWorkspaceGraphLayout default: %v", err)
	}
	if len(defaultLayout) != 1 || defaultLayout[0].NodeID != ids["default-coder"] {
		t.Fatalf("default layout = %+v, want default-coder only", defaultLayout)
	}
	teamLayout, err := store.ReadWorkspaceGraphLayout(db, "team-a")
	if err != nil {
		t.Fatalf("ReadWorkspaceGraphLayout team-a: %v", err)
	}
	if len(teamLayout) != 1 || teamLayout[0].NodeID != ids["team-coder"] {
		t.Fatalf("team layout = %+v, want team-coder only", teamLayout)
	}

	if err := store.UpsertWorkspaceGraphLayout(db, "team-a", []store.GraphNodePosition{{NodeID: ids["default-coder"], X: 9, Y: 9}}); err == nil {
		t.Fatal("UpsertWorkspaceGraphLayout accepted agent from another workspace")
	}
}

func TestGraphLayoutDeletedWithAgent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	for _, name := range []string{"coder", "reviewer"} {
		if err := store.UpsertAgent(db, fleet.Agent{Name: name, Backend: "claude", PromptRef: "coder", Description: name + " agent", Skills: []string{}, CanDispatch: []string{}}); err != nil {
			t.Fatalf("UpsertAgent %s: %v", name, err)
		}
	}
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	var coderID string
	for _, a := range agents {
		if a.Name == "coder" {
			coderID = a.ID
			break
		}
	}
	if coderID == "" {
		t.Fatal("coder ID is empty")
	}
	if err := store.UpsertGraphLayout(db, []store.GraphNodePosition{{NodeID: coderID, X: 1, Y: 2}}); err != nil {
		t.Fatalf("UpsertGraphLayout: %v", err)
	}
	if err := store.DeleteAgent(db, "coder"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	got, err := store.ReadGraphLayout(db)
	if err != nil {
		t.Fatalf("ReadGraphLayout: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("layout after agent delete = %+v, want empty", got)
	}
}

func TestDeleteAgent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	// Seed two agents so that deleting one still leaves the system valid.
	for _, name := range []string{"coder", "reviewer"} {
		if err := store.UpsertAgent(db, fleet.Agent{
			Name: name, Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
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
	db := openTestDB(t)

	if err := store.DeleteAgent(db, "ghost"); err != nil {
		t.Errorf("DeleteAgent non-existent: %v", err)
	}
}

// ──── Skills ─────────────────────────────────────────────────────────────────

func TestUpsertAndReadSkills(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	s := fleet.Skill{Prompt: "Focus on architecture."}
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
	if skills["architect"].Version != 1 || skills["architect"].VersionID == "" {
		t.Fatalf("skill version = (%q, %d), want published v1", skills["architect"].VersionID, skills["architect"].Version)
	}
}

func TestCatalogUpsertsPublishImmutableVersions(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "versioned-coder", Description: "first", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt v1: %v", err)
	}
	if prompt.Version != 1 || prompt.VersionID == "" {
		t.Fatalf("prompt v1 = (%q, %d), want version id and number 1", prompt.VersionID, prompt.Version)
	}
	prompt.Content = "body v2"
	prompt, err = store.UpsertPrompt(db, prompt)
	if err != nil {
		t.Fatalf("UpsertPrompt v2: %v", err)
	}
	if prompt.Version != 2 || prompt.VersionID == "" {
		t.Fatalf("prompt v2 = (%q, %d), want version id and number 2", prompt.VersionID, prompt.Version)
	}
	var versions int
	if err := db.QueryRow("SELECT COUNT(*) FROM prompt_versions WHERE prompt_id=(SELECT id FROM prompts WHERE ref=?)", prompt.ID).Scan(&versions); err != nil {
		t.Fatalf("count prompt_versions: %v", err)
	}
	if versions != 2 {
		t.Fatalf("prompt_versions count = %d, want 2", versions)
	}

	if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "skill v1"}); err != nil {
		t.Fatalf("UpsertSkill v1: %v", err)
	}
	if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "skill v2"}); err != nil {
		t.Fatalf("UpsertSkill v2: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if got := skills["architect"].Version; got != 2 {
		t.Fatalf("skill version = %d, want 2", got)
	}
}

func TestPromptDraftDoesNotAffectCurrentUntilPublished(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "draftable", Description: "first", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin draft: %v", err)
	}
	draft, err := store.CreatePromptDraftTx(tx, prompt.ID, "draft", "body v2")
	if err != nil {
		tx.Rollback()
		t.Fatalf("CreatePromptDraftTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	if draft.State != "draft" || draft.Version != 2 || draft.ID == "" {
		t.Fatalf("draft = (%q, %d, %q), want draft v2 with id", draft.State, draft.Version, draft.ID)
	}

	current, err := store.ReadPrompt(db, prompt.ID)
	if err != nil {
		t.Fatalf("ReadPrompt before publish: %v", err)
	}
	if current.Content != "body v1" || current.Version != 1 {
		t.Fatalf("current before publish = v%d %q, want v1 body", current.Version, current.Content)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("begin publish: %v", err)
	}
	published, err := store.PublishPromptVersionTx(tx, draft.ID)
	if err != nil {
		tx.Rollback()
		t.Fatalf("PublishPromptVersionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit publish: %v", err)
	}
	if published.Content != "body v2" || published.Version != 2 || published.VersionID != draft.ID {
		t.Fatalf("published = v%d id=%q content=%q, want draft promoted", published.Version, published.VersionID, published.Content)
	}
	current, err = store.ReadPrompt(db, prompt.ID)
	if err != nil {
		t.Fatalf("ReadPrompt after publish: %v", err)
	}
	if current.Content != "body v2" || current.VersionID != draft.ID {
		t.Fatalf("current after publish = id=%q content=%q, want draft current", current.VersionID, current.Content)
	}
	versions, err := store.ListPromptVersions(db, prompt.ID)
	if err != nil {
		t.Fatalf("ListPromptVersions: %v", err)
	}
	if len(versions) != 2 || versions[0].ID != draft.ID || versions[0].State != "published" || versions[1].Version != 1 {
		t.Fatalf("versions = %#v, want published draft first and original second", versions)
	}
}

func TestCatalogDraftPublishPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		createAsset   func(*testing.T, *sql.DB) string
		createDraft   func(*testing.T, *sql.Tx, string) fleet.CatalogVersion
		publishDraft  func(*testing.T, *sql.Tx, string)
		readCurrent   func(*testing.T, *sql.DB, string) (string, string)
		currentBody   string
		publishedBody string
	}{
		{
			name: "skill",
			createAsset: func(t *testing.T, db *sql.DB) string {
				t.Helper()
				if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "skill v1"}); err != nil {
					t.Fatalf("UpsertSkill: %v", err)
				}
				return "architect"
			},
			createDraft: func(t *testing.T, tx *sql.Tx, ref string) fleet.CatalogVersion {
				t.Helper()
				draft, err := store.CreateSkillDraftTx(tx, ref, "skill v2")
				if err != nil {
					t.Fatalf("CreateSkillDraftTx: %v", err)
				}
				return draft
			},
			publishDraft: func(t *testing.T, tx *sql.Tx, versionID string) {
				t.Helper()
				if _, _, err := store.PublishSkillVersionTx(tx, versionID); err != nil {
					t.Fatalf("PublishSkillVersionTx: %v", err)
				}
			},
			readCurrent: func(t *testing.T, db *sql.DB, ref string) (string, string) {
				t.Helper()
				skills, err := store.ReadSkills(db)
				if err != nil {
					t.Fatalf("ReadSkills: %v", err)
				}
				return skills[ref].VersionID, skills[ref].Prompt
			},
			currentBody:   "skill v1",
			publishedBody: "skill v2",
		},
		{
			name: "guardrail",
			createAsset: func(t *testing.T, db *sql.DB) string {
				t.Helper()
				g := fleet.Guardrail{Name: "security-review", Description: "v1", Content: "guardrail v1", Enabled: true, Position: 10}
				if err := store.UpsertGuardrail(db, g); err != nil {
					t.Fatalf("UpsertGuardrail: %v", err)
				}
				return "security-review"
			},
			createDraft: func(t *testing.T, tx *sql.Tx, ref string) fleet.CatalogVersion {
				t.Helper()
				g := fleet.Guardrail{Name: ref, Description: "v2", Content: "guardrail v2", Enabled: true, Position: 20}
				draft, err := store.CreateGuardrailDraftTx(tx, ref, g)
				if err != nil {
					t.Fatalf("CreateGuardrailDraftTx: %v", err)
				}
				return draft
			},
			publishDraft: func(t *testing.T, tx *sql.Tx, versionID string) {
				t.Helper()
				if _, err := store.PublishGuardrailVersionTx(tx, versionID); err != nil {
					t.Fatalf("PublishGuardrailVersionTx: %v", err)
				}
			},
			readCurrent: func(t *testing.T, db *sql.DB, ref string) (string, string) {
				t.Helper()
				g, err := store.GetGuardrail(db, ref)
				if err != nil {
					t.Fatalf("GetGuardrail: %v", err)
				}
				return g.VersionID, g.Content
			},
			currentBody:   "guardrail v1",
			publishedBody: "guardrail v2",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			ref := tc.createAsset(t, db)
			originalVersionID, body := tc.readCurrent(t, db, ref)
			if body != tc.currentBody {
				t.Fatalf("initial body = %q, want %q", body, tc.currentBody)
			}

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin draft: %v", err)
			}
			draft := tc.createDraft(t, tx, ref)
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit draft: %v", err)
			}
			if draft.State != "draft" || draft.Version != 2 || draft.ID == "" {
				t.Fatalf("draft = (%q, %d, %q), want draft v2 with id", draft.State, draft.Version, draft.ID)
			}
			versionID, body := tc.readCurrent(t, db, ref)
			if versionID != originalVersionID || body != tc.currentBody {
				t.Fatalf("current after draft = (%q, %q), want original (%q, %q)", versionID, body, originalVersionID, tc.currentBody)
			}

			tx, err = db.Begin()
			if err != nil {
				t.Fatalf("begin publish: %v", err)
			}
			tc.publishDraft(t, tx, draft.ID)
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit publish: %v", err)
			}
			versionID, body = tc.readCurrent(t, db, ref)
			if versionID != draft.ID || body != tc.publishedBody {
				t.Fatalf("current after publish = (%q, %q), want draft (%q, %q)", versionID, body, draft.ID, tc.publishedBody)
			}
		})
	}
}

func TestCatalogVersionReferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "skill v1"}); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "security-review", Description: "v1", Content: "guardrail v1", Enabled: true, Position: 10}); err != nil {
		t.Fatalf("UpsertGuardrail: %v", err)
	}

	promptRef := "prompt_coder"
	promptV1 := currentPromptVersionID(t, db, promptRef)
	skillV1 := currentSkillVersionID(t, db, "architect")
	guardrailV1 := currentGuardrailVersionID(t, db, "security-review")

	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "tracking-agent", Backend: "claude", PromptRef: "coder", Skills: []string{"architect"}, Description: "tracks current",
	}); err != nil {
		t.Fatalf("UpsertAgent tracking-agent: %v", err)
	}
	if _, err := store.ReplaceWorkspaceGuardrails(db, fleet.DefaultWorkspaceID, []fleet.WorkspaceGuardrailRef{{
		GuardrailName: "security-review", Position: 10, Enabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceWorkspaceGuardrails default: %v", err)
	}

	promptV2 := publishPromptDraft(t, db, promptRef, "v2", "prompt v2")
	skillV2 := publishSkillDraft(t, db, "architect", "skill v2")
	guardrailV2 := publishGuardrailDraft(t, db, "security-review", fleet.Guardrail{Name: "security-review", Description: "v2", Content: "guardrail v2", Enabled: true, Position: 20})

	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "pinned-agent", Backend: "claude", PromptRef: "coder", PromptVersionID: promptV1, Skills: []string{"architect@1"}, Description: "pins old versions",
	}); err != nil {
		t.Fatalf("UpsertAgent pinned-agent: %v", err)
	}
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	if _, err := store.ReplaceWorkspaceGuardrails(db, "team-a", []fleet.WorkspaceGuardrailRef{{
		GuardrailName: "security-review", GuardrailVersionID: guardrailV1, Position: 10, Enabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceWorkspaceGuardrails team-a: %v", err)
	}

	assertVersionRefs(t, "prompt v1", mustPromptRefs(t, db, promptRef, promptV1), []fleet.CatalogVersionReference{{
		Kind: "agent", WorkspaceID: fleet.DefaultWorkspaceID, Name: "pinned-agent", Reference: "prompt", VersionID: promptV1, Tracking: false,
	}})
	assertVersionRefs(t, "prompt v2", mustPromptRefs(t, db, promptRef, promptV2), []fleet.CatalogVersionReference{{
		Kind: "agent", WorkspaceID: fleet.DefaultWorkspaceID, Name: "tracking-agent", Reference: "prompt", VersionID: promptV2, Tracking: true,
	}})
	assertVersionRefs(t, "skill v1", mustSkillRefs(t, db, "architect", skillV1), []fleet.CatalogVersionReference{{
		Kind: "agent", WorkspaceID: fleet.DefaultWorkspaceID, Name: "pinned-agent", Reference: "skill", VersionID: skillV1, Tracking: false,
	}})
	assertVersionRefs(t, "skill v2", mustSkillRefs(t, db, "architect", skillV2), []fleet.CatalogVersionReference{{
		Kind: "agent", WorkspaceID: fleet.DefaultWorkspaceID, Name: "tracking-agent", Reference: "skill", VersionID: skillV2, Tracking: true,
	}})
	assertVersionRefs(t, "guardrail v1", mustGuardrailRefs(t, db, "security-review", guardrailV1), []fleet.CatalogVersionReference{{
		Kind: "workspace", WorkspaceID: "team-a", Name: "team-a", Reference: "guardrail", VersionID: guardrailV1, Tracking: false,
	}})
	assertVersionRefs(t, "guardrail v2", mustGuardrailRefs(t, db, "security-review", guardrailV2), []fleet.CatalogVersionReference{{
		Kind: "workspace", WorkspaceID: fleet.DefaultWorkspaceID, Name: fleet.DefaultWorkspaceID, Reference: "guardrail", VersionID: guardrailV2, Tracking: true,
	}})
}

func TestUpgradeCatalogVersionReferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "skill v1"}); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "security-review", Description: "v1", Content: "guardrail v1", Enabled: true, Position: 10}); err != nil {
		t.Fatalf("UpsertGuardrail: %v", err)
	}

	promptRef := "prompt_coder"
	promptV1 := currentPromptVersionID(t, db, promptRef)
	skillV1 := currentSkillVersionID(t, db, "architect")
	guardrailV1 := currentGuardrailVersionID(t, db, "security-review")
	promptV2 := publishPromptDraft(t, db, promptRef, "v2", "prompt v2")
	skillV2 := publishSkillDraft(t, db, "architect", "skill v2")
	guardrailV2 := publishGuardrailDraft(t, db, "security-review", fleet.Guardrail{Name: "security-review", Description: "v2", Content: "guardrail v2", Enabled: true, Position: 20})

	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "pinned-agent", Backend: "claude", PromptRef: "coder", PromptVersionID: promptV1, Skills: []string{"architect@1"}, Description: "pins old versions",
	}); err != nil {
		t.Fatalf("UpsertAgent pinned-agent: %v", err)
	}
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	if _, err := store.ReplaceWorkspaceGuardrails(db, "team-a", []fleet.WorkspaceGuardrailRef{{
		GuardrailName: "security-review", GuardrailVersionID: guardrailV1, Position: 10, Enabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceWorkspaceGuardrails: %v", err)
	}

	tests := []struct {
		name    string
		upgrade func() (fleet.CatalogVersionRolloutResult, error)
		noop    func() (fleet.CatalogVersionRolloutResult, error)
		oldRefs func() []fleet.CatalogVersionReference
		newRefs func() []fleet.CatalogVersionReference
	}{
		{
			name: "prompt",
			upgrade: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradePromptVersionReferences(db, promptRef, promptV1, promptV2)
			},
			noop: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradePromptVersionReferences(db, promptRef, promptV2, promptV2)
			},
			oldRefs: func() []fleet.CatalogVersionReference { return mustPromptRefs(t, db, promptRef, promptV1) },
			newRefs: func() []fleet.CatalogVersionReference { return mustPromptRefs(t, db, promptRef, promptV2) },
		},
		{
			name: "skill",
			upgrade: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradeSkillVersionReferences(db, "architect", skillV1, skillV2)
			},
			noop: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradeSkillVersionReferences(db, "architect", skillV2, skillV2)
			},
			oldRefs: func() []fleet.CatalogVersionReference { return mustSkillRefs(t, db, "architect", skillV1) },
			newRefs: func() []fleet.CatalogVersionReference { return mustSkillRefs(t, db, "architect", skillV2) },
		},
		{
			name: "guardrail",
			upgrade: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradeGuardrailVersionReferences(db, "security-review", guardrailV1, guardrailV2)
			},
			noop: func() (fleet.CatalogVersionRolloutResult, error) {
				return store.UpgradeGuardrailVersionReferences(db, "security-review", guardrailV2, guardrailV2)
			},
			oldRefs: func() []fleet.CatalogVersionReference {
				return mustGuardrailRefs(t, db, "security-review", guardrailV1)
			},
			newRefs: func() []fleet.CatalogVersionReference {
				return mustGuardrailRefs(t, db, "security-review", guardrailV2)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.upgrade()
			if err != nil {
				t.Fatalf("upgrade %s refs: %v", tc.name, err)
			}
			if result.Updated != 1 {
				t.Fatalf("updated = %d, want 1", result.Updated)
			}
			if refs := tc.oldRefs(); len(refs) != 0 {
				t.Fatalf("old refs after upgrade = %#v, want none", refs)
			}
			exact := slices.ContainsFunc(tc.newRefs(), func(ref fleet.CatalogVersionReference) bool {
				return !ref.Tracking
			})
			if !exact {
				t.Fatalf("new refs after upgrade have no exact pin")
			}
			result, err = tc.noop()
			if err != nil {
				t.Fatalf("noop upgrade %s refs: %v", tc.name, err)
			}
			if result.Updated != 0 {
				t.Fatalf("noop updated = %d, want 0", result.Updated)
			}
		})
	}
}

func TestUpgradeCatalogVersionReferencesRejectUnknownWrongOrUnpublished(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	promptA, err := store.UpsertPrompt(db, fleet.Prompt{Name: "prompt-a", Content: "a"})
	if err != nil {
		t.Fatalf("UpsertPrompt prompt-a: %v", err)
	}
	promptB, err := store.UpsertPrompt(db, fleet.Prompt{Name: "prompt-b", Content: "b"})
	if err != nil {
		t.Fatalf("UpsertPrompt prompt-b: %v", err)
	}
	if err := store.UpsertSkill(db, "skill-a", fleet.Skill{Prompt: "a"}); err != nil {
		t.Fatalf("UpsertSkill skill-a: %v", err)
	}
	if err := store.UpsertSkill(db, "skill-b", fleet.Skill{Prompt: "b"}); err != nil {
		t.Fatalf("UpsertSkill skill-b: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "guardrail-a", Description: "a", Content: "a", Enabled: true, Position: 10}); err != nil {
		t.Fatalf("UpsertGuardrail guardrail-a: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "guardrail-b", Description: "b", Content: "b", Enabled: true, Position: 20}); err != nil {
		t.Fatalf("UpsertGuardrail guardrail-b: %v", err)
	}

	promptADraft := createPromptDraft(t, db, promptA.ID, "draft", "draft")
	skillADraft := createSkillDraft(t, db, "skill-a", "draft")
	guardrailADraft := createGuardrailDraft(t, db, "guardrail-a", fleet.Guardrail{Name: "guardrail-a", Description: "draft", Content: "draft", Enabled: true, Position: 30})
	skillAV1 := currentSkillVersionID(t, db, "skill-a")
	skillBV1 := currentSkillVersionID(t, db, "skill-b")
	guardrailAV1 := currentGuardrailVersionID(t, db, "guardrail-a")
	guardrailBV1 := currentGuardrailVersionID(t, db, "guardrail-b")

	tests := []struct {
		name    string
		upgrade func() error
	}{
		{
			name: "unknown prompt from version",
			upgrade: func() error {
				_, err := store.UpgradePromptVersionReferences(db, promptA.ID, "missing-version", promptA.VersionID)
				return err
			},
		},
		{
			name: "unknown prompt to version",
			upgrade: func() error {
				_, err := store.UpgradePromptVersionReferences(db, promptA.ID, promptA.VersionID, "missing-version")
				return err
			},
		},
		{
			name: "wrong prompt from asset",
			upgrade: func() error {
				_, err := store.UpgradePromptVersionReferences(db, promptA.ID, promptB.VersionID, promptA.VersionID)
				return err
			},
		},
		{
			name: "wrong prompt to asset",
			upgrade: func() error {
				_, err := store.UpgradePromptVersionReferences(db, promptA.ID, promptA.VersionID, promptB.VersionID)
				return err
			},
		},
		{
			name: "unpublished prompt target",
			upgrade: func() error {
				_, err := store.UpgradePromptVersionReferences(db, promptA.ID, promptA.VersionID, promptADraft)
				return err
			},
		},
		{
			name: "unknown skill from version",
			upgrade: func() error {
				_, err := store.UpgradeSkillVersionReferences(db, "skill-a", "missing-version", skillAV1)
				return err
			},
		},
		{
			name: "unknown skill to version",
			upgrade: func() error {
				_, err := store.UpgradeSkillVersionReferences(db, "skill-a", skillAV1, "missing-version")
				return err
			},
		},
		{
			name: "wrong skill from asset",
			upgrade: func() error {
				_, err := store.UpgradeSkillVersionReferences(db, "skill-a", skillBV1, skillAV1)
				return err
			},
		},
		{
			name: "wrong skill to asset",
			upgrade: func() error {
				_, err := store.UpgradeSkillVersionReferences(db, "skill-a", skillAV1, skillBV1)
				return err
			},
		},
		{
			name: "unpublished skill target",
			upgrade: func() error {
				_, err := store.UpgradeSkillVersionReferences(db, "skill-a", skillAV1, skillADraft)
				return err
			},
		},
		{
			name: "unknown guardrail from version",
			upgrade: func() error {
				_, err := store.UpgradeGuardrailVersionReferences(db, "guardrail-a", "missing-version", guardrailAV1)
				return err
			},
		},
		{
			name: "unknown guardrail to version",
			upgrade: func() error {
				_, err := store.UpgradeGuardrailVersionReferences(db, "guardrail-a", guardrailAV1, "missing-version")
				return err
			},
		},
		{
			name: "wrong guardrail from asset",
			upgrade: func() error {
				_, err := store.UpgradeGuardrailVersionReferences(db, "guardrail-a", guardrailBV1, guardrailAV1)
				return err
			},
		},
		{
			name: "wrong guardrail to asset",
			upgrade: func() error {
				_, err := store.UpgradeGuardrailVersionReferences(db, "guardrail-a", guardrailAV1, guardrailBV1)
				return err
			},
		},
		{
			name: "unpublished guardrail target",
			upgrade: func() error {
				_, err := store.UpgradeGuardrailVersionReferences(db, "guardrail-a", guardrailAV1, guardrailADraft)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var notFound *store.ErrNotFound
			if err := tc.upgrade(); !errors.As(err, &notFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestCatalogVersionReferencesRejectUnknownOrWrongAsset(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	promptA, err := store.UpsertPrompt(db, fleet.Prompt{Name: "prompt-a", Content: "a"})
	if err != nil {
		t.Fatalf("UpsertPrompt prompt-a: %v", err)
	}
	promptB, err := store.UpsertPrompt(db, fleet.Prompt{Name: "prompt-b", Content: "b"})
	if err != nil {
		t.Fatalf("UpsertPrompt prompt-b: %v", err)
	}
	if err := store.UpsertSkill(db, "skill-a", fleet.Skill{Prompt: "a"}); err != nil {
		t.Fatalf("UpsertSkill skill-a: %v", err)
	}
	if err := store.UpsertSkill(db, "skill-b", fleet.Skill{Prompt: "b"}); err != nil {
		t.Fatalf("UpsertSkill skill-b: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "guardrail-a", Description: "a", Content: "a", Enabled: true, Position: 10}); err != nil {
		t.Fatalf("UpsertGuardrail guardrail-a: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "guardrail-b", Description: "b", Content: "b", Enabled: true, Position: 20}); err != nil {
		t.Fatalf("UpsertGuardrail guardrail-b: %v", err)
	}

	skillBVersionID := currentSkillVersionID(t, db, "skill-b")
	guardrailBVersionID := currentGuardrailVersionID(t, db, "guardrail-b")
	tests := []struct {
		name string
		list func() error
	}{
		{
			name: "unknown prompt version",
			list: func() error {
				_, err := store.ListPromptVersionReferences(db, promptA.ID, "missing-version")
				return err
			},
		},
		{
			name: "wrong prompt asset",
			list: func() error {
				_, err := store.ListPromptVersionReferences(db, promptA.ID, promptB.VersionID)
				return err
			},
		},
		{
			name: "unknown skill version",
			list: func() error {
				_, err := store.ListSkillVersionReferences(db, "skill-a", "missing-version")
				return err
			},
		},
		{
			name: "wrong skill asset",
			list: func() error {
				_, err := store.ListSkillVersionReferences(db, "skill-a", skillBVersionID)
				return err
			},
		},
		{
			name: "unknown guardrail version",
			list: func() error {
				_, err := store.ListGuardrailVersionReferences(db, "guardrail-a", "missing-version")
				return err
			},
		},
		{
			name: "wrong guardrail asset",
			list: func() error {
				_, err := store.ListGuardrailVersionReferences(db, "guardrail-a", guardrailBVersionID)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var notFound *store.ErrNotFound
			if err := tc.list(); !errors.As(err, &notFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

func currentPromptVersionID(t *testing.T, db *sql.DB, ref string) string {
	t.Helper()
	prompt, err := store.ReadPrompt(db, ref)
	if err != nil {
		t.Fatalf("ReadPrompt %s: %v", ref, err)
	}
	return prompt.VersionID
}

func currentSkillVersionID(t *testing.T, db *sql.DB, ref string) string {
	t.Helper()
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	return skills[ref].VersionID
}

func currentGuardrailVersionID(t *testing.T, db *sql.DB, ref string) string {
	t.Helper()
	guardrail, err := store.GetGuardrail(db, ref)
	if err != nil {
		t.Fatalf("GetGuardrail %s: %v", ref, err)
	}
	return guardrail.VersionID
}

func publishPromptDraft(t *testing.T, db *sql.DB, ref, description, content string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin prompt draft: %v", err)
	}
	draft, err := store.CreatePromptDraftTx(tx, ref, description, content)
	if err != nil {
		t.Fatalf("CreatePromptDraftTx: %v", err)
	}
	if _, err := store.PublishPromptVersionTx(tx, draft.ID); err != nil {
		t.Fatalf("PublishPromptVersionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit prompt draft: %v", err)
	}
	return draft.ID
}

func createPromptDraft(t *testing.T, db *sql.DB, ref, description, content string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin prompt draft: %v", err)
	}
	draft, err := store.CreatePromptDraftTx(tx, ref, description, content)
	if err != nil {
		t.Fatalf("CreatePromptDraftTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit prompt draft: %v", err)
	}
	return draft.ID
}

func publishSkillDraft(t *testing.T, db *sql.DB, ref, prompt string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin skill draft: %v", err)
	}
	draft, err := store.CreateSkillDraftTx(tx, ref, prompt)
	if err != nil {
		t.Fatalf("CreateSkillDraftTx: %v", err)
	}
	if _, _, err := store.PublishSkillVersionTx(tx, draft.ID); err != nil {
		t.Fatalf("PublishSkillVersionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit skill draft: %v", err)
	}
	return draft.ID
}

func createSkillDraft(t *testing.T, db *sql.DB, ref, prompt string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin skill draft: %v", err)
	}
	draft, err := store.CreateSkillDraftTx(tx, ref, prompt)
	if err != nil {
		t.Fatalf("CreateSkillDraftTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit skill draft: %v", err)
	}
	return draft.ID
}

func publishGuardrailDraft(t *testing.T, db *sql.DB, ref string, guardrail fleet.Guardrail) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin guardrail draft: %v", err)
	}
	draft, err := store.CreateGuardrailDraftTx(tx, ref, guardrail)
	if err != nil {
		t.Fatalf("CreateGuardrailDraftTx: %v", err)
	}
	if _, err := store.PublishGuardrailVersionTx(tx, draft.ID); err != nil {
		t.Fatalf("PublishGuardrailVersionTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit guardrail draft: %v", err)
	}
	return draft.ID
}

func createGuardrailDraft(t *testing.T, db *sql.DB, ref string, guardrail fleet.Guardrail) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin guardrail draft: %v", err)
	}
	draft, err := store.CreateGuardrailDraftTx(tx, ref, guardrail)
	if err != nil {
		t.Fatalf("CreateGuardrailDraftTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit guardrail draft: %v", err)
	}
	return draft.ID
}

func mustPromptRefs(t *testing.T, db *sql.DB, ref, versionID string) []fleet.CatalogVersionReference {
	t.Helper()
	refs, err := store.ListPromptVersionReferences(db, ref, versionID)
	if err != nil {
		t.Fatalf("ListPromptVersionReferences: %v", err)
	}
	return refs
}

func mustSkillRefs(t *testing.T, db *sql.DB, ref, versionID string) []fleet.CatalogVersionReference {
	t.Helper()
	refs, err := store.ListSkillVersionReferences(db, ref, versionID)
	if err != nil {
		t.Fatalf("ListSkillVersionReferences: %v", err)
	}
	return refs
}

func mustGuardrailRefs(t *testing.T, db *sql.DB, ref, versionID string) []fleet.CatalogVersionReference {
	t.Helper()
	refs, err := store.ListGuardrailVersionReferences(db, ref, versionID)
	if err != nil {
		t.Fatalf("ListGuardrailVersionReferences: %v", err)
	}
	return refs
}

func assertVersionRefs(t *testing.T, name string, got, want []fleet.CatalogVersionReference) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s refs = %#v, want %#v", name, got, want)
	}
}

func TestPublishCatalogVersionRejectsAlreadyPublished(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "already-published", Description: "first", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin publish: %v", err)
	}
	_, err = store.PublishPromptVersionTx(tx, prompt.VersionID)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback publish: %v", rollbackErr)
	}
	var validationErr *store.ErrValidation
	if !errors.As(err, &validationErr) {
		t.Fatalf("PublishPromptVersionTx already-published error = %v, want ErrValidation", err)
	}
}

func TestPublishCatalogVersionRejectsStaleDraft(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "stale-draft", Description: "first", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt v1: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin draft: %v", err)
	}
	draft, err := store.CreatePromptDraftTx(tx, prompt.ID, "draft", "body v2")
	if err != nil {
		tx.Rollback()
		t.Fatalf("CreatePromptDraftTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	prompt.Content = "body v3"
	if _, err := store.UpsertPrompt(db, prompt); err != nil {
		t.Fatalf("UpsertPrompt v3: %v", err)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("begin stale publish: %v", err)
	}
	_, err = store.PublishPromptVersionTx(tx, draft.ID)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback stale publish: %v", rollbackErr)
	}
	var validationErr *store.ErrValidation
	if !errors.As(err, &validationErr) {
		t.Fatalf("PublishPromptVersionTx stale error = %v, want ErrValidation", err)
	}

	current, err := store.ReadPrompt(db, prompt.ID)
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if current.Content != "body v3" || current.Version != 3 {
		t.Fatalf("current after stale publish = v%d %q, want v3 body", current.Version, current.Content)
	}
}

func TestUpsertScopedSkillDerivesStableID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if _, err := db.Exec(`
		INSERT OR IGNORE INTO workspaces(id, name) VALUES('platform', 'Platform');
		INSERT OR IGNORE INTO repos(workspace_id, name, enabled) VALUES('platform', 'eloylp/agents', 1);
	`); err != nil {
		t.Fatalf("seed repo scope: %v", err)
	}

	s := fleet.Skill{
		WorkspaceID: "Platform",
		Repo:        "EloyLP/Agents",
		Name:        "Review",
		Prompt:      "p",
	}
	if err := store.UpsertSkill(db, "", s); err != nil {
		t.Fatalf("UpsertSkill scoped without id: %v", err)
	}
	if err := store.UpsertSkill(db, "", fleet.Skill{
		WorkspaceID: "platform",
		Repo:        "eloylp/agents",
		Name:        "review",
		Prompt:      "updated",
	}); err != nil {
		t.Fatalf("UpsertSkill scoped update without id: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	got, ok := skills["skill_platform_eloylp_agents_review"]
	if !ok {
		t.Fatalf("derived scoped skill missing; keys=%v", slices.Sorted(maps.Keys(skills)))
	}
	if got.WorkspaceID != "platform" || got.Repo != "eloylp/agents" || got.Name != "review" || got.Prompt != "updated" {
		t.Fatalf("scoped skill = %+v, want normalized updated skill", got)
	}
}

func TestDeleteSkill(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	if err := store.UpsertSkill(db, "architect", fleet.Skill{Prompt: "p"}); err != nil {
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
	db := openTestDB(t)

	b := fleet.Backend{
		Command:        "claude",
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
	if got.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds: got %d, want 300", got.TimeoutSeconds)
	}
}

func TestUpsertBackendAppliesDefaults(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Persist a backend with zero numeric fields, the same payload that
	// POST /api/store/backends would send when omitting timeout_seconds and
	// max_prompt_chars from the request body.
	b := fleet.Backend{Command: "claude"}
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
	db := openTestDB(t)

	// Seed two backends so that deleting one still leaves the system valid.
	for _, name := range []string{"claude", "codex"} {
		if err := store.UpsertBackend(db, name, fleet.Backend{
			Command: name,
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	// UpsertRepo requires the agents referenced by bindings to exist.
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	enabled := true
	r := fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use: []fleet.Binding{
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	r := fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use: []fleet.Binding{
			{Agent: "coder", Labels: []string{"ai:fix"}},
			{Agent: "coder", Cron: "0 9 * * *"},
		},
	}
	if err := store.UpsertRepo(db, r); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Re-upsert with only one binding.
	r.Use = []fleet.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}}
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Seed two repos so that deleting one still leaves at least one enabled.
	for _, name := range []string{"owner/repo", "owner/other"} {
		if err := store.UpsertRepo(db, fleet.Repo{
			Name:    name,
			Enabled: true,
			Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	// Seed one agent and one repo.
	a := fleet.Agent{
		Name:        "coder",
		Backend:     "claude",
		Skills:      []string{},
		PromptRef:   "coder",
		Description: "coder agent",
	}
	if err := store.UpsertAgent(db, a); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	enabled := true
	r := fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder", Events: []string{"issues.labeled"}, Enabled: &enabled}},
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
		agent   fleet.Agent
		wantErr string
	}{
		{
			name:    "unknown backend",
			setup:   func(t *testing.T, db *sql.DB) { t.Helper() }, // no backend seeded
			agent:   fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{}},
			wantErr: "unknown backend",
		},
		{
			name:    "unknown skill",
			setup:   func(t *testing.T, db *sql.DB) { seedBackend(t, db, "claude") },
			agent:   fleet.Agent{Name: "coder", Backend: "claude", PromptRef: "coder", Description: "coder agent", Skills: []string{"architect"}},
			wantErr: "unknown skill",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
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
	db := openTestDB(t)

	// No agent seeded, binding references "ghost". The FK constraint on
	// bindings.agent may fire first, or ValidateEntities catches it; either
	// way an error must be returned and nothing must be committed.
	err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "ghost", Labels: []string{"ai:fix"}}},
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
	db := openTestDB(t)

	// Seed two backends so that the "at least one backend" constraint is not the
	// reason the delete fails, only the agent reference should block it.
	seedBackend(t, db, "claude")
	seedBackend(t, db, "codex")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:        "coder",
		Backend:     "claude",
		PromptRef:   "coder",
		Description: "coder agent",
		Skills:      []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Deleting "claude" while "coder" references it must fail (codex remains).
	err := store.DeleteBackend(db, "claude")
	if err == nil {
		t.Fatal("DeleteBackend still referenced by agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), `backend "claude" is referenced by 1 agent(s): default/coder`) {
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	seedSkill(t, db, "architect")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:        "coder",
		Backend:     "claude",
		PromptRef:   "coder",
		Description: "coder agent",
		Skills:      []string{"architect"},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Deleting "architect" while "coder" references it must fail.
	err := store.DeleteSkill(db, "architect")
	if err == nil {
		t.Fatal("DeleteSkill still referenced by agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), `skill "architect" is referenced by 1 agent(s): default/coder`) {
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

func TestDeleteAgentRejectedWhenBindingReferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	for _, name := range []string{"coder", "reviewer"} {
		if err := store.UpsertAgent(db, fleet.Agent{
			Name: name, Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
		}); err != nil {
			t.Fatalf("UpsertAgent %s: %v", name, err)
		}
	}
	enabled := true
	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:fix"}, Enabled: &enabled}},
	}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	// Non-cascade delete must fail while a binding references the agent. The
	// error must be ErrConflict so the HTTP layer can return 409 rather than
	// leaking a raw FK constraint as 500.
	err := store.DeleteAgent(db, "coder")
	if err == nil {
		t.Fatal("DeleteAgent with live bindings: want error, got nil")
	}
	var conflict *store.ErrConflict
	if !errors.As(err, &conflict) {
		t.Errorf("DeleteAgent with live bindings: want *store.ErrConflict, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "still referenced") {
		t.Errorf("error message should explain the blocker: %v", err)
	}

	// Agent must still be present.
	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("agent count after rejected delete: got %d, want 2", len(agents))
	}
}

func TestDeleteAgentCascadeRemovesBindings(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	for _, name := range []string{"coder", "reviewer"} {
		if err := store.UpsertAgent(db, fleet.Agent{
			Name: name, Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
		}); err != nil {
			t.Fatalf("UpsertAgent %s: %v", name, err)
		}
	}
	enabled := true
	// Repo keeps a binding for "reviewer" so the cascade path does not wipe
	// the repo entirely; only bindings referencing "coder" should disappear.
	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use: []fleet.Binding{
			{Agent: "coder", Labels: []string{"ai:fix"}, Enabled: &enabled},
			{Agent: "coder", Events: []string{"issues.opened"}, Enabled: &enabled},
			{Agent: "reviewer", Labels: []string{"ai:review"}, Enabled: &enabled},
		},
	}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	if err := store.DeleteAgentCascade(db, "coder"); err != nil {
		t.Fatalf("DeleteAgentCascade: %v", err)
	}

	agents, err := store.ReadAgents(db)
	if err != nil {
		t.Fatalf("ReadAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "reviewer" {
		t.Errorf("agents after cascade: got %v, want [reviewer]", agents)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos after cascade: got %d, want 1", len(repos))
	}
	if len(repos[0].Use) != 1 {
		t.Fatalf("bindings after cascade: got %d, want 1", len(repos[0].Use))
	}
	if repos[0].Use[0].Agent != "reviewer" {
		t.Errorf("surviving binding agent: got %q, want %q", repos[0].Use[0].Agent, "reviewer")
	}
}

func TestDeleteAgentCascadeStillRejectsLastAgent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	err := store.DeleteAgentCascade(db, "coder")
	if err == nil {
		t.Fatal("DeleteAgentCascade last agent: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one agent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeleteAgentCascadeStillRejectsWhenInCanDispatch(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "target", Backend: "claude", PromptRef: "coder", Description: "a dispatchable target",
		AllowDispatch: true, Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent target: %v", err)
	}
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "dispatcher", Backend: "claude", PromptRef: "coder",
		Description: "test agent",
		Skills:      []string{}, CanDispatch: []string{"target"},
	}); err != nil {
		t.Fatalf("UpsertAgent dispatcher: %v", err)
	}

	// Cascade is scoped to bindings; dangling can_dispatch references must
	// still block the delete so callers cannot silently reshape the dispatch
	// graph by deleting a referenced target.
	if err := store.DeleteAgentCascade(db, "target"); err == nil {
		t.Fatal("DeleteAgentCascade referenced by can_dispatch: want error, got nil")
	}
}

func TestDeleteAgentRejectedWhenDispatchListReferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")

	// Seed two agents: "dispatcher" can_dispatch to "target".
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:          "target",
		Backend:       "claude",
		PromptRef:     "coder",
		Description:   "a dispatchable target",
		AllowDispatch: true,
		Skills:        []string{},
		CanDispatch:   []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent target: %v", err)
	}
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:        "dispatcher",
		Backend:     "claude",
		PromptRef:   "coder",
		Description: "dispatches work",
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
		cfg     fleet.Backend
		wantErr string
	}{
		{
			name:    "empty command",
			bName:   "claude",
			cfg:     fleet.Backend{Command: ""},
			wantErr: "command is required",
		},
		{
			name:    "invalid name",
			bName:   "unknown-ai",
			cfg:     fleet.Backend{Command: "ai"},
			wantErr: "unsupported ai backend",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
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
	db := openTestDB(t)

	err := store.UpsertSkill(db, "testing", fleet.Skill{Prompt: ""})
	if err == nil {
		t.Fatal("UpsertSkill with empty prompt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpsertAgentRejectedWithoutPromptRef(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	err := store.UpsertAgent(db, fleet.Agent{
		Name:    "coder",
		Backend: "claude",
		Skills:  []string{},
	})
	if err == nil {
		t.Fatal("UpsertAgent without prompt reference: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt_id or prompt_ref is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpsertRepoRejectedWithNoTrigger(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	// Binding has no labels, events, or cron, invalid.
	err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder"}},
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	// Binding mixes labels and events, invalid.
	err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use: []fleet.Binding{{
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
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
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
	db := openTestDB(t)

	if err := store.UpsertBackend(db, "claude", fleet.Backend{
		Command: "claude",
	}); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}

	err := store.DeleteBackend(db, "claude")
	if err == nil {
		t.Fatal("DeleteBackend last backend: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least one backend") {
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

// TestDeleteRepoAllowsLastEnabled verifies that DeleteRepo succeeds even when
// it removes the last (or only) enabled repo. Disabling/removing all repos is
// a legitimate user action; the daemon runs cleanly with zero enabled repos.
// Regression for issue #302.
func TestDeleteRepoAllowsLastEnabled(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:fix"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	if err := store.DeleteRepo(db, "owner/repo"); err != nil {
		t.Fatalf("DeleteRepo last enabled repo: want nil, got %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("repo count after delete: got %d, want 0", len(repos))
	}

	// Bindings for the deleted repo must also be gone.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM bindings WHERE repo='owner/repo'").Scan(&count); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if count != 0 {
		t.Errorf("orphan bindings after DeleteRepo: got %d, want 0", count)
	}
}

// ──── Normalization ───────────────────────────────────────────────────────────

// TestUpsertNormalizesNames verifies that UpsertAgent, UpsertSkill,
// UpsertBackend, UpsertPrompt, and UpsertRepo all lowercase+trim entity keys before writing
// to SQLite. This ensures the stored form matches what FinishLoad would
// produce at startup, so AgentByName lookups and registerJobs cron bindings
// never silently diverge from the persisted rows after a live CRUD write.
func TestUpsertNormalizesNames(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Backend, mixed-case name should be stored lowercase.
	if err := store.UpsertBackend(db, "Claude", fleet.Backend{
		Command: "claude",
	}); err != nil {
		t.Fatalf("UpsertBackend: %v", err)
	}
	backends, err := store.ReadBackends(db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	if _, ok := backends["claude"]; !ok {
		t.Errorf("backend name not normalised: got keys %v, want 'claude'", slices.Sorted(maps.Keys(backends)))
	}
	if _, bad := backends["Claude"]; bad {
		t.Error("original mixed-case key 'Claude' should not be present after normalisation")
	}

	// Prompt, mixed-case name should be stored lowercase.
	if _, err := store.UpsertPrompt(db, fleet.Prompt{Name: "Release-Notes", Content: "p"}); err != nil {
		t.Fatalf("UpsertPrompt: %v", err)
	}
	prompts, err := store.ReadPrompts(db)
	if err != nil {
		t.Fatalf("ReadPrompts: %v", err)
	}
	if !slices.ContainsFunc(prompts, func(p fleet.Prompt) bool { return p.Name == "release-notes" }) {
		t.Errorf("prompt name not normalised: got %+v, want release-notes", prompts)
	}
	if slices.ContainsFunc(prompts, func(p fleet.Prompt) bool { return p.Name == "Release-Notes" }) {
		t.Error("original mixed-case prompt name 'Release-Notes' should not be present after normalisation")
	}

	// Skill, mixed-case key should be stored lowercase.
	if err := store.UpsertSkill(db, "Architect", fleet.Skill{Prompt: "p"}); err != nil {
		t.Fatalf("UpsertSkill: %v", err)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if _, ok := skills["architect"]; !ok {
		t.Errorf("skill name not normalised: got keys %v, want 'architect'", slices.Sorted(maps.Keys(skills)))
	}

	// Agent, mixed-case name, backend, and skill reference should be stored lowercase.
	if err := store.UpsertAgent(db, fleet.Agent{
		Name:        "Coder",
		Backend:     "Claude",
		PromptRef:   "coder",
		Description: "coder agent",
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

	// Repo, binding agent name should be stored lowercase.
	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "Coder", Labels: []string{"ai:fix"}}},
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

// TestUpsertSkillNormalizesPrompt verifies that UpsertSkill trims Prompt
// before persisting, matching the normalization startup applies. A
// whitespace-only prompt must be trimmed to "" and then rejected by
// validation, otherwise the write API would persist state that the daemon
// refuses to load on restart.
func TestUpsertSkillNormalizesPrompt(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Whitespace-only prompt should be trimmed to "" and rejected.
	err := store.UpsertSkill(db, "testing", fleet.Skill{Prompt: "   "})
	if err == nil {
		t.Fatal("UpsertSkill with whitespace-only prompt: want error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("unexpected error message: %v", err)
	}

	// A prompt with surrounding whitespace should be trimmed and stored cleanly.
	if err := store.UpsertSkill(db, "testing", fleet.Skill{Prompt: "  skill guidance  "}); err != nil {
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
func TestUpsertBackendNormalizesCommand(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Whitespace-only command must be trimmed to "" and rejected.
	err := store.UpsertBackend(db, "claude", fleet.Backend{
		Command: "   ",
	})
	if err == nil {
		t.Fatal("UpsertBackend with whitespace-only command: want error, got nil")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("unexpected error: %v", err)
	}

	// Padded command should be stored trimmed.
	if err := store.UpsertBackend(db, "claude", fleet.Backend{
		Command: "  claude  ",
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
}

// ──── Bindings (atomic per-item CRUD) ────────────────────────────────────────

// seedRepoWithAgent bootstraps a repo + agent pair with no bindings so the
// binding tests can exercise CRUD directly.
func seedRepoWithAgent(t *testing.T, db *sql.DB) {
	t.Helper()
	seedBackend(t, db, "claude")
	if err := store.UpsertAgent(db, fleet.Agent{
		Name: "coder", Backend: "claude", PromptRef: "coder", Description: "test agent", Skills: []string{}, CanDispatch: []string{},
	}); err != nil {
		t.Fatalf("UpsertAgent coder: %v", err)
	}
	if err := store.UpsertRepo(db, fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:seed"}}},
	}); err != nil {
		t.Fatalf("UpsertRepo owner/repo: %v", err)
	}
}

func TestCreateBinding(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	id, persisted, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent:  "coder",
		Labels: []string{"ai:fix"},
	})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected non-zero id, got %d", id)
	}
	if persisted.ID != id {
		t.Errorf("persisted.ID=%d, want %d", persisted.ID, id)
	}

	repoName, got, found, err := store.ReadBinding(db, id)
	if err != nil || !found {
		t.Fatalf("ReadBinding: found=%v err=%v", found, err)
	}
	if repoName != "owner/repo" || got.Agent != "coder" {
		t.Errorf("got repo=%q agent=%q", repoName, got.Agent)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "ai:fix" {
		t.Errorf("labels: %v", got.Labels)
	}
}

func TestCreateBindingInvalidTrigger(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	// No trigger at all.
	_, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{Agent: "coder"})
	var valErr *store.ErrValidation
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ErrValidation for missing trigger, got %v", err)
	}

	// Mixed triggers.
	_, _, err = store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Labels: []string{"a"}, Cron: "* * * * *",
	})
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ErrValidation for mixed triggers, got %v", err)
	}

	// Bad cron.
	_, _, err = store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Cron: "bogus",
	})
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ErrValidation for bad cron, got %v", err)
	}
}

func TestCreateBindingUnknownRepo(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	_, _, err := store.CreateBinding(db, "owner/missing", fleet.Binding{
		Agent: "coder", Labels: []string{"a"},
	})
	var nf *store.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateBindingUnknownAgent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	_, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "ghost", Labels: []string{"a"},
	})
	var valErr *store.ErrValidation
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ErrValidation for unknown agent, got %v", err)
	}
}

func TestUpdateBinding(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	id, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Labels: []string{"ai:old"},
	})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}

	disabled := false
	updated, err := store.UpdateBinding(db, id, fleet.Binding{
		Agent:   "coder",
		Cron:    "0 9 * * *",
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	if updated.ID != id {
		t.Errorf("id: got %d, want %d", updated.ID, id)
	}
	if updated.Cron != "0 9 * * *" || len(updated.Labels) != 0 {
		t.Errorf("not replaced: %+v", updated)
	}

	_, got, found, err := store.ReadBinding(db, id)
	if err != nil || !found {
		t.Fatalf("ReadBinding: found=%v err=%v", found, err)
	}
	if got.IsEnabled() {
		t.Errorf("expected disabled, got enabled")
	}
}

func TestUpdateBindingNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	_, err := store.UpdateBinding(db, 99999, fleet.Binding{
		Agent: "coder", Labels: []string{"x"},
	})
	var nf *store.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteBinding(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	id, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Labels: []string{"ai:gone"},
	})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if err := store.DeleteBinding(db, id); err != nil {
		t.Fatalf("DeleteBinding: %v", err)
	}
	_, _, found, err := store.ReadBinding(db, id)
	if err != nil {
		t.Fatalf("ReadBinding: %v", err)
	}
	if found {
		t.Fatalf("expected binding to be gone")
	}
}

func TestDeleteBindingNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	err := store.DeleteBinding(db, 99999)
	var nf *store.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReadBindingExposesIDViaLoadRepos(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedRepoWithAgent(t, db)

	id1, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Labels: []string{"ai:a"},
	})
	if err != nil {
		t.Fatalf("CreateBinding 1: %v", err)
	}
	id2, _, err := store.CreateBinding(db, "owner/repo", fleet.Binding{
		Agent: "coder", Cron: "0 * * * *",
	})
	if err != nil {
		t.Fatalf("CreateBinding 2: %v", err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	idx := slices.IndexFunc(repos, func(r fleet.Repo) bool { return r.Name == "owner/repo" })
	if idx == -1 {
		t.Fatalf("repo not found")
	}
	r := &repos[idx]
	// First binding was seeded by seedRepoWithAgent; the two additions we
	// expect as id1/id2 below it.
	seen := map[int64]struct{}{}
	for _, b := range r.Use {
		if b.ID == 0 {
			t.Errorf("binding has zero id: %+v", b)
		}
		seen[b.ID] = struct{}{}
	}
	_, ok1 := seen[id1]
	_, ok2 := seen[id2]
	if !ok1 || !ok2 {
		t.Errorf("created ids not surfaced: got %v, want ids %d + %d", seen, id1, id2)
	}
}
