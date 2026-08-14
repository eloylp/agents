package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/catalog"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type fakeCatalogGitHub struct {
	head      string
	file      []byte
	blobSHA   string
	writeSHA  string
	writeErr  error
	writes    int
	reads     []string
	lastWrite []byte
}

func (f *fakeCatalogGitHub) BranchHead(context.Context, catalogGitHubConfig) (string, error) {
	if f.head == "" {
		return "base123", nil
	}
	return f.head, nil
}

func (f *fakeCatalogGitHub) ReadFile(_ context.Context, _ catalogGitHubConfig, ref string) ([]byte, string, error) {
	f.reads = append(f.reads, ref)
	if f.file == nil {
		return nil, "", &githubStatusError{StatusCode: 404, Body: "not found"}
	}
	return f.file, f.blobSHA, nil
}

func (f *fakeCatalogGitHub) WriteFile(_ context.Context, _ catalogGitHubConfig, _ string, content []byte, _ string) (string, error) {
	f.writes++
	f.lastWrite = append([]byte(nil), content...)
	if f.writeErr != nil {
		return "", f.writeErr
	}
	if f.writeSHA == "" {
		return "commit123", nil
	}
	return f.writeSHA, nil
}

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

func TestApplyDelegatedCatalogReconcilesScopedAssets(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)
	if err := svc.UpsertSkill("old-workspace-skill", fleet.Skill{
		ID:          "old-workspace-skill",
		WorkspaceID: fleet.DefaultWorkspaceID,
		Name:        "old-workspace-skill",
		Prompt:      "old",
	}); err != nil {
		t.Fatalf("seed scoped skill: %v", err)
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
    body: updated global prompt
  - id: repo-coder
    kind: prompt
    workspace_id: default
    repo: owner/repo
    name: coder
    body: repo prompt
  - id: workspace-skill
    kind: skill
    workspace_id: default
    name: workspace skill
    body: workspace skill
  - id: workspace-guardrail
    kind: guardrail
    workspace_id: default
    name: workspace guardrail
    body: workspace guardrail
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := svc.ApplyDelegatedCatalog(file, "abc123"); err != nil {
		t.Fatalf("ApplyDelegatedCatalog: %v", err)
	}

	prompt, err := store.ReadPrompt(db, "repo-coder")
	if err != nil {
		t.Fatalf("ReadPrompt scoped: %v", err)
	}
	if prompt.WorkspaceID != fleet.DefaultWorkspaceID || prompt.Repo != "owner/repo" || prompt.Content != "repo prompt" {
		t.Fatalf("scoped prompt = %+v, want default owner/repo repo prompt", prompt)
	}
	skills, err := store.ReadSkills(db)
	if err != nil {
		t.Fatalf("ReadSkills: %v", err)
	}
	if _, ok := skills["old-workspace-skill"]; ok {
		t.Fatalf("old-workspace-skill still present after delegated reconcile")
	}
	if got := skills["workspace-skill"]; got.WorkspaceID != fleet.DefaultWorkspaceID || got.Prompt != "workspace skill" {
		t.Fatalf("workspace-skill = %+v, want scoped updated skill", got)
	}
	guardrail, err := store.GetGuardrail(db, "workspace-guardrail")
	if err != nil {
		t.Fatalf("GetGuardrail scoped: %v", err)
	}
	if guardrail.WorkspaceID != fleet.DefaultWorkspaceID || guardrail.Content != "workspace guardrail" {
		t.Fatalf("workspace guardrail = %+v, want scoped content", guardrail)
	}
}

func TestActivateCatalogDelegationExportsBeforeEnabling(t *testing.T) {
	_, db := openTestService(t)
	fake := &fakeCatalogGitHub{writeSHA: "commit456"}
	svc := NewWithCatalogGitHub(store.New(db), fake)
	enabled := true
	repo := "owner/catalog"
	credentialRef := "AGENTS_TEST_CATALOG_TOKEN_EXPORT"
	t.Setenv(credentialRef, "secret-token")

	cfg, err := svc.ActivateCatalogDelegation(context.Background(), store.CatalogDelegationPatch{
		Enabled:       &enabled,
		Repo:          &repo,
		CredentialRef: &credentialRef,
	}, "")
	if err != nil {
		t.Fatalf("ActivateCatalogDelegation: %v", err)
	}
	if !cfg.Enabled || cfg.LastSyncedCommit != "commit456" || cfg.LastSyncStatus != "synced" {
		t.Fatalf("delegation cfg = %+v, want enabled synced at commit456", cfg)
	}
	if fake.writes != 1 {
		t.Fatalf("writes = %d, want 1", fake.writes)
	}
	if got := string(fake.lastWrite); !strings.Contains(got, "id: coder") || strings.Contains(got, "secret-token") {
		t.Fatalf("exported catalog = %q, want coder asset and no credential", got)
	}
}

func TestActivateCatalogDelegationFailureLeavesDisabled(t *testing.T) {
	_, db := openTestService(t)
	fake := &fakeCatalogGitHub{writeErr: errors.New("write failed")}
	svc := NewWithCatalogGitHub(store.New(db), fake)
	enabled := true
	repo := "owner/catalog"
	credentialRef := "AGENTS_TEST_CATALOG_TOKEN_FAILURE"
	t.Setenv(credentialRef, "secret-token")

	err := func() error {
		_, err := svc.ActivateCatalogDelegation(context.Background(), store.CatalogDelegationPatch{
			Enabled:       &enabled,
			Repo:          &repo,
			CredentialRef: &credentialRef,
		}, "")
		return err
	}()
	if err == nil {
		t.Fatal("ActivateCatalogDelegation err = nil, want error")
	}
	cfg, readErr := store.ReadCatalogDelegationConfig(db)
	if readErr != nil {
		t.Fatalf("ReadCatalogDelegationConfig: %v", readErr)
	}
	if cfg.Enabled || cfg.LastSyncedCommit != "" || cfg.LastSyncStatus != "error" {
		t.Fatalf("delegation cfg = %+v, want disabled error without synced commit", cfg)
	}
}

func TestActivateCatalogDelegationRequiresExplicitReenableModeWhenDiverged(t *testing.T) {
	_, db := openTestService(t)
	enabled := true
	disabled := false
	repo := "owner/catalog"
	credentialRef := "AGENTS_TEST_CATALOG_TOKEN_REENABLE"
	t.Setenv(credentialRef, "secret-token")
	sha := "old123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
		CredentialRef:    &credentialRef,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig enable: %v", err)
	}
	svc := New(store.New(db))
	if _, err := svc.PatchCatalogDelegationConfig(store.CatalogDelegationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig disable: %v", err)
	}
	disabledCfg, err := store.ReadCatalogDelegationConfig(db)
	if err != nil {
		t.Fatalf("ReadCatalogDelegationConfig after disable: %v", err)
	}
	if disabledCfg.DisabledAt == "" {
		t.Fatal("disabled_at after disable is empty")
	}
	fake := &fakeCatalogGitHub{head: "new123", file: []byte(`
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: github prompt
`)}
	svc = NewWithCatalogGitHub(store.New(db), fake)

	_, err = svc.ActivateCatalogDelegation(context.Background(), store.CatalogDelegationPatch{
		Enabled: &enabled,
		Repo:    &repo,
	}, "")
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("ActivateCatalogDelegation error = %T %v, want ErrValidation", err, err)
	}
	if fake.writes != 0 {
		t.Fatalf("writes = %d, want 0", fake.writes)
	}
	cfg, err := store.ReadCatalogDelegationConfig(db)
	if err != nil {
		t.Fatalf("ReadCatalogDelegationConfig: %v", err)
	}
	if cfg.DisabledAt != disabledCfg.DisabledAt {
		t.Fatalf("disabled_at = %q, want preserved %q", cfg.DisabledAt, disabledCfg.DisabledAt)
	}
}

func TestActivateCatalogDelegationResumeFromRepoAppliesRemoteCatalog(t *testing.T) {
	_, db := openTestService(t)
	enabled := true
	disabled := false
	repo := "owner/catalog"
	credentialRef := "AGENTS_TEST_CATALOG_TOKEN_RESUME"
	t.Setenv(credentialRef, "secret-token")
	sha := "old123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
		CredentialRef:    &credentialRef,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig enable: %v", err)
	}
	svc := New(store.New(db))
	if _, err := svc.PatchCatalogDelegationConfig(store.CatalogDelegationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig disable: %v", err)
	}
	fake := &fakeCatalogGitHub{head: "new123", file: []byte(`
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: github prompt
`)}
	svc = NewWithCatalogGitHub(store.New(db), fake)

	cfg, err := svc.ActivateCatalogDelegation(context.Background(), store.CatalogDelegationPatch{
		Enabled: &enabled,
		Repo:    &repo,
	}, catalogDelegationResumeFromRepo)
	if err != nil {
		t.Fatalf("ActivateCatalogDelegation: %v", err)
	}
	if !cfg.Enabled || cfg.LastSyncedCommit != "new123" {
		t.Fatalf("delegation cfg = %+v, want enabled at new123", cfg)
	}
	if fake.writes != 0 {
		t.Fatalf("writes = %d, want 0", fake.writes)
	}
	prompt, err := store.ReadPrompt(db, "coder")
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if prompt.Content != "github prompt" {
		t.Fatalf("prompt content = %q, want github prompt", prompt.Content)
	}
}

func TestPatchCatalogDelegationConfigDisableRecordsAuditState(t *testing.T) {
	t.Parallel()
	svc, db := openTestService(t)
	enabled := true
	disabled := false
	repo := "owner/catalog"
	sha := "abc123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig enable: %v", err)
	}

	cfg, err := svc.PatchCatalogDelegationConfig(store.CatalogDelegationPatch{Enabled: &disabled})
	if err != nil {
		t.Fatalf("PatchCatalogDelegationConfig disable: %v", err)
	}
	if cfg.Enabled || cfg.DisabledAt == "" || cfg.LastSyncStatus != "disabled" {
		t.Fatalf("delegation cfg = %+v, want disabled with audit status", cfg)
	}
}

func TestSyncDelegatedCatalogAppliesChangedHead(t *testing.T) {
	_, db := openTestService(t)
	enabled := true
	repo := "owner/catalog"
	credentialRef := "AGENTS_TEST_CATALOG_TOKEN_SYNC"
	t.Setenv(credentialRef, "secret-token")
	sha := "old123"
	if _, err := store.PatchCatalogDelegationConfig(db, store.CatalogDelegationPatch{
		Enabled:          &enabled,
		Repo:             &repo,
		LastSyncedCommit: &sha,
		CredentialRef:    &credentialRef,
	}); err != nil {
		t.Fatalf("PatchCatalogDelegationConfig: %v", err)
	}
	svc := NewWithCatalogGitHub(store.New(db), &fakeCatalogGitHub{
		head: "new123",
		file: []byte(`
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: synced prompt
`),
	})

	cfg, err := svc.SyncDelegatedCatalog(context.Background())
	if err != nil {
		t.Fatalf("SyncDelegatedCatalog: %v", err)
	}
	if cfg.LastSyncedCommit != "new123" || cfg.LastSyncStatus != "synced" {
		t.Fatalf("delegation cfg = %+v, want new123 synced", cfg)
	}
	prompt, err := store.ReadPrompt(db, "coder")
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if prompt.Content != "synced prompt" {
		t.Fatalf("prompt content = %q, want synced prompt", prompt.Content)
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
