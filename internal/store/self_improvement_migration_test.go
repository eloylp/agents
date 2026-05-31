package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestSelfImprovementAnalystMigrationPreservesCustomizedCurrentVersion(t *testing.T) {
	t.Parallel()

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { st.Close() })

	prompt, err := st.ReadPrompt("prompt_self-improvement-analyst")
	if err != nil {
		t.Fatalf("read seeded prompt: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	custom, err := CreatePromptDraftTx(tx, prompt.ID, "Custom analyst", "custom analyst body", fleet.CatalogVersionMetadata{Changelog: "operator customization"})
	if err != nil {
		t.Fatalf("create custom draft: %v", err)
	}
	if _, err := PublishPromptVersionTx(tx, custom.ID); err != nil {
		t.Fatalf("publish custom version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit custom version: %v", err)
	}

	migration, err := os.ReadFile(filepath.Join("migrations", "038_self_improvement_recommendations.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	got, err := st.ReadPrompt("prompt_self-improvement-analyst")
	if err != nil {
		t.Fatalf("read prompt after rerun: %v", err)
	}
	if got.VersionID != custom.ID || got.Content != "custom analyst body" {
		t.Fatalf("prompt after rerun = version %q body %q, want custom version %q", got.VersionID, got.Content, custom.ID)
	}
}

func TestSelfImprovementAnalystMigrationUsesCanonicalBodyHash(t *testing.T) {
	t.Parallel()

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { st.Close() })

	prompt, err := st.ReadPrompt("prompt_self-improvement-analyst")
	if err != nil {
		t.Fatalf("read seeded prompt: %v", err)
	}
	var bodyHash string
	if err := db.QueryRow(`SELECT body_hash FROM prompt_versions WHERE id = ?`, prompt.VersionID).Scan(&bodyHash); err != nil {
		t.Fatalf("read body hash: %v", err)
	}
	if want := catalogBodyHash(prompt.Description, prompt.Content); bodyHash != want {
		t.Fatalf("body_hash = %q, want canonical %q", bodyHash, want)
	}
}
