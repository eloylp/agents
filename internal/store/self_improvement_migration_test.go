package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestSelfImprovementAnalystPromptV8BecomesCurrentOnFreshStore(t *testing.T) {
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
	if prompt.VersionID != "promptver_self_improvement_analyst_v8" {
		t.Fatalf("version_id = %q, want v8", prompt.VersionID)
	}
	for _, want := range []string{
		"Supplied context:",
		"Maintainer-directed feedback:",
		"Catalog design heuristics:",
		"intelligence cluster",
		"ambiguity debt",
		"Bundle recommendations:",
		"catalog_patch_bundle",
		"Implementation-guidance examples:",
		"prefer short code examples over abstract natural language",
		"Editable proposal contract:",
		"Every status=recommended catalog-changing result must be directly reviewable",
		"proposed_body must be the full replacement body",
		"Catalog context is attribution-only.",
		"return status=needs_user_input instead of scanning or guessing from the wider catalog",
		"changes, and no_auto_apply_confirmed",
	} {
		if !strings.Contains(prompt.Content, want) {
			t.Fatalf("prompt content missing %q", want)
		}
	}
	if strings.Contains(prompt.Content, "knowledge cluster") {
		t.Fatal("prompt content still contains knowledge cluster")
	}
	if strings.Contains(prompt.Content, "suggested_rollout_scope") {
		t.Fatal("prompt content still contains suggested_rollout_scope")
	}
}

func TestSelfImprovementLegacyFieldsRemovedOnFreshStore(t *testing.T) {
	t.Parallel()

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { st.Close() })

	rows, err := db.Query(`PRAGMA table_info(self_improvement_recommendations)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		if name == "suggested_rollout_scope" {
			t.Fatal("self_improvement_recommendations still has suggested_rollout_scope")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table info: %v", err)
	}

	feedback, err := st.UpsertSelfImprovementFeedback(SelfImprovementFeedbackInput{
		WorkspaceID:      fleet.DefaultWorkspaceID,
		RepoOwner:        "owner",
		RepoName:         "repo",
		SourceType:       "issue_comment",
		GitHubCommentID:  53,
		SourceURL:        "https://github.com/owner/repo/issues/1#issuecomment-53",
		AuthorLogin:      "maintainer",
		AuthorAuthorized: true,
		IssueNumber:      1,
		RawBody:          "cleanup schema /agents improve",
		Tag:              FeedbackTag,
		LinkConfidence:   "exact",
	})
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO self_improvement_recommendations (
			id, workspace_id, feedback_event_id, type, status, confidence, risk,
			finding, normalized_lesson, rationale, evidence_feedback_ids,
			evidence_source_urls, attribution_confidence, structured_output
		) VALUES (?, ?, ?, 'catalog_patch_bundle', 'recommended', 'medium', 'low',
			'finding', 'lesson', 'rationale', ?, 'https://github.com/owner/repo/issues/1#issuecomment-53',
			'exact', '{}'
		)
	`, "rec_no_pending_decision", fleet.DefaultWorkspaceID, feedback.ID, feedback.ID); err != nil {
		t.Fatalf("seed recommendation: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO self_improvement_proposal_bundles (id, workspace_id, recommendation_id)
		VALUES ('bundle_no_pending_decision', ?, 'rec_no_pending_decision')
	`, fleet.DefaultWorkspaceID); err != nil {
		t.Fatalf("seed bundle: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO self_improvement_proposal_bundle_items (id, bundle_id, operation, asset_type, decision, proposed_body)
		VALUES ('item_no_pending_decision', 'bundle_no_pending_decision', 'update_existing', 'skill', 'pending', 'body')
	`); err == nil {
		t.Fatal("insert pending proposal bundle item decision succeeded, want CHECK failure")
	}
}

func TestAcceptedRecommendationCleanupMigration(t *testing.T) {
	t.Parallel()

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { st.Close() })

	feedback, err := st.UpsertSelfImprovementFeedback(SelfImprovementFeedbackInput{
		WorkspaceID:      fleet.DefaultWorkspaceID,
		RepoOwner:        "owner",
		RepoName:         "repo",
		SourceType:       "issue_comment",
		GitHubCommentID:  47,
		SourceURL:        "https://github.com/owner/repo/issues/1#issuecomment-47",
		AuthorLogin:      "maintainer",
		AuthorAuthorized: true,
		IssueNumber:      1,
		RawBody:          "old accepted recommendation",
		Tag:              FeedbackTag,
		LinkConfidence:   "exact",
	})
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO self_improvement_recommendations (
			id, workspace_id, feedback_event_id, type, status, confidence, risk,
			finding, normalized_lesson, rationale, evidence_feedback_ids,
			evidence_source_urls, attribution_confidence, structured_output
		) VALUES (?, ?, ?, 'catalog_patch', 'accepted', 'medium', 'low',
			'finding', 'lesson', 'rationale', ?, 'https://github.com/owner/repo/issues/1#issuecomment-47',
			'exact', '{}'
		)
	`, "rec_old_accepted", fleet.DefaultWorkspaceID, feedback.ID, feedback.ID); err != nil {
		t.Fatalf("seed accepted recommendation: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE name = '047_remove_accepted_recommendation_status.sql'`); err != nil {
		t.Fatalf("unmark cleanup migration: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("rerun cleanup migration: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM self_improvement_recommendations WHERE id = 'rec_old_accepted'`).Scan(&status); err != nil {
		t.Fatalf("read recommendation status: %v", err)
	}
	if status != "recommended" {
		t.Fatalf("status = %q, want recommended", status)
	}
}

func TestSelfImprovementAnalystPromptV5MigrationPreservesCustomizedCurrentVersion(t *testing.T) {
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
	custom, err := CreatePublishedPromptVersionTx(tx, prompt.ID, "Custom analyst", "custom analyst body", fleet.CatalogVersionMetadata{Changelog: "operator customization"})
	if err != nil {
		t.Fatalf("create custom version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit custom version: %v", err)
	}

	migration, err := os.ReadFile(filepath.Join("migrations", "045_self_improvement_analyst_prompt_v5_intelligence_language.sql"))
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
	custom, err := CreatePublishedPromptVersionTx(tx, prompt.ID, "Custom analyst", "custom analyst body", fleet.CatalogVersionMetadata{Changelog: "operator customization"})
	if err != nil {
		t.Fatalf("create custom version: %v", err)
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
