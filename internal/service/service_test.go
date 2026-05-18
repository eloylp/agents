package service

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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

	if _, err := store.UpsertPrompt(db, fleet.Prompt{Name: "coder", Content: "test prompt"}); err != nil {
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
