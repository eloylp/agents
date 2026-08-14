package service

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/catalog"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func openTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := store.UpsertPrompt(db, fleet.Prompt{ID: "coder", Name: "coder", Content: "test prompt"}); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if err := store.UpsertBackend(db, "claude", fleet.Backend{Command: "claude"}); err != nil {
		t.Fatalf("seed backend: %v", err)
	}
	svc := New(store.New(db))
	if err := svc.UpsertAgent(fleet.Agent{
		Name:        "coder",
		Backend:     "claude",
		PromptRef:   "coder",
		Description: "Writes code",
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := svc.UpsertRepo(fleet.Repo{Name: "owner/repo", Enabled: true}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return svc, db
}

func TestCreateWorkspaceBindingRejectsInvalidShape(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)

	_, _, err := svc.CreateWorkspaceBinding(fleet.DefaultWorkspaceID, "owner/repo", fleet.Binding{
		Agent:  "coder",
		Labels: []string{"ai ready"},
		Events: []string{"issues.opened"},
	})
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("CreateWorkspaceBinding err = %T %v, want ErrValidation", err, err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if got := len(repos[0].Use); got != 0 {
		t.Fatalf("binding count = %d, want 0", got)
	}
}

func TestCatalogMutationsBlockedWhenDelegated(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)
	enabled := true
	repo := "owner/catalog"
	sha := "abc123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "prompt upsert",
			run: func() error {
				_, err := svc.UpsertPrompt(fleet.Prompt{ID: "new-prompt", Name: "new-prompt", Content: "body"})
				return err
			},
		},
		{
			name: "prompt delete",
			run:  func() error { return svc.DeletePrompt("coder") },
		},
		{
			name: "skill upsert",
			run:  func() error { return svc.UpsertSkill("new-skill", fleet.Skill{Name: "new-skill", Prompt: "body"}) },
		},
		{
			name: "guardrail upsert",
			run: func() error {
				return svc.UpsertGuardrail(fleet.Guardrail{ID: "new-guardrail", Name: "new-guardrail", Content: "body"})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var delegated *store.ErrCatalogDelegated
			if !errors.As(err, &delegated) {
				t.Fatalf("%s error = %T %v, want ErrCatalogDelegated", tc.name, err, err)
			}
		})
	}
}

func TestReplaceConfigPreservesDelegatedCatalogMirror(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)
	enabled := true
	repo := "owner/catalog"
	sha := "abc123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig: %v", err)
	}

	err := svc.ReplaceConfig(&config.Config{
		Backends: map[string]fleet.Backend{"claude": {Command: "claude"}},
		Agents: []fleet.Agent{{
			Name:        "coder",
			Backend:     "claude",
			PromptRef:   "coder",
			Description: "Writes code",
		}},
		Repos: []fleet.Repo{{Name: "owner/repo", Enabled: true}},
	}, nil)
	if err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	prompt, err := store.ReadPrompt(db, "coder")
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if prompt.Content != "test prompt" {
		t.Fatalf("prompt content = %q, want test prompt", prompt.Content)
	}
}

func TestApplyDelegatedCatalogUpdatesMirrorAndCommitSHA(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)
	if err := svc.UpsertSkill("go-api", fleet.Skill{ID: "go-api", Name: "go-api", Prompt: "old"}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	enabled := true
	repo := "owner/catalog"
	sha := "base123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig: %v", err)
	}

	file, err := catalog.Parse([]byte(`
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: updated prompt
  - id: go-api
    kind: skill
    name: go-api
    body: updated skill
  - id: rollout
    kind: guardrail
    name: rollout
    description: release safety
    enabled: true
    position: 20
    body: deploy carefully
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := svc.ApplyDelegatedCatalog(file, "abc123"); err != nil {
		t.Fatalf("ApplyDelegatedCatalog: %v", err)
	}

	prompt, err := store.ReadPrompt(db, "coder")
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if prompt.Content != "updated prompt" {
		t.Fatalf("prompt content = %q, want updated prompt", prompt.Content)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if skills["go-api"].Prompt != "updated skill" {
		t.Fatalf("skill prompt = %q, want updated skill", skills["go-api"].Prompt)
	}
	guardrail, err := store.GetGuardrail(db, "rollout")
	if err != nil {
		t.Fatalf("GetGuardrail: %v", err)
	}
	if !guardrail.Enabled || guardrail.Position != 20 {
		t.Fatalf("guardrail = %+v, want enabled position 20", guardrail)
	}
	cfg, err := store.ReadCatalogDelegationConfig(db)
	if err != nil {
		t.Fatalf("ReadCatalogDelegationConfig: %v", err)
	}
	if cfg.LastSyncedCommit != "abc123" || cfg.LastSyncStatus != "synced" {
		t.Fatalf("delegation cfg = %+v, want sha abc123 synced", cfg)
	}
}

func TestUpsertRepoRejectsInvalidCronBeforePersisting(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)

	err := svc.UpsertRepo(fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use: []fleet.Binding{{
			Agent: "coder",
			Cron:  "not a cron",
		}},
	})
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("UpsertRepo err = %T %v, want ErrValidation", err, err)
	}

	repos, err := store.ReadRepos(db)
	if err != nil {
		t.Fatalf("ReadRepos: %v", err)
	}
	if got := len(repos[0].Use); got != 0 {
		t.Fatalf("binding count = %d, want 0", got)
	}
}
