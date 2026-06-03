package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestSelfImprovementAnalystPromptV3BecomesCurrentOnFreshStore(t *testing.T) {
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
	if prompt.VersionID != "promptver_self_improvement_analyst_v3" {
		t.Fatalf("version_id = %q, want v3", prompt.VersionID)
	}
	for _, want := range []string{
		"Supplied context:",
		"Maintainer-directed feedback:",
		"Catalog design heuristics:",
		"knowledge cluster",
		"ambiguity debt",
		"Bundle recommendations:",
		"catalog_patch_bundle",
	} {
		if !strings.Contains(prompt.Content, want) {
			t.Fatalf("prompt content missing %q", want)
		}
	}
}

func TestSelfImprovementAnalystPromptV3MigrationPreservesCustomizedCurrentVersion(t *testing.T) {
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

	migration, err := os.ReadFile(filepath.Join("migrations", "042_self_improvement_analyst_prompt_v3_bundles.sql"))
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

func TestSelfImprovementAnalystPromptV3UpgradePreservesPreExistingCustomization(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "test.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')));
		INSERT INTO schema_migrations(name) VALUES ('039_self_improvement_analyst_prompt_v2.sql');
	`); err != nil {
		t.Fatalf("mark v2 migration applied: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { st.Close() })

	prompt, err := st.ReadPrompt("prompt_self-improvement-analyst")
	if err != nil {
		t.Fatalf("read seeded prompt: %v", err)
	}
	if prompt.VersionID != "promptver_self_improvement_analyst_v1" {
		t.Fatalf("version_id before upgrade = %q, want v1", prompt.VersionID)
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
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE name = '039_self_improvement_analyst_prompt_v2.sql'`); err != nil {
		t.Fatalf("unmark v2 migration: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("apply v2 migration: %v", err)
	}

	got, err := st.ReadPrompt("prompt_self-improvement-analyst")
	if err != nil {
		t.Fatalf("read prompt after upgrade: %v", err)
	}
	if got.VersionID != custom.ID || got.Content != "custom analyst body" {
		t.Fatalf("prompt after upgrade = version %q body %q, want custom version %q", got.VersionID, got.Content, custom.ID)
	}
	var v2Count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v2'`).Scan(&v2Count); err != nil {
		t.Fatalf("count v2 prompt version: %v", err)
	}
	if v2Count != 1 {
		t.Fatalf("v2 prompt version count = %d, want 1", v2Count)
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
