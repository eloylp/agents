package selfimprovement

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func openServiceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newServiceTest(t *testing.T) (*Service, *store.Store, *sql.DB) {
	t.Helper()
	db := openServiceTestDB(t)
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	return New(st), st, db
}

func seedFeedback(t *testing.T, st *store.Store, workspace string, id int64) store.SelfImprovementFeedback {
	t.Helper()
	if workspace != "" && workspace != fleet.DefaultWorkspaceID {
		if _, err := st.UpsertWorkspace(fleet.Workspace{ID: workspace, Name: workspace}); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	feedback, err := st.UpsertSelfImprovementFeedback(store.SelfImprovementFeedbackInput{
		WorkspaceID:      workspace,
		RepoOwner:        "owner",
		RepoName:         "repo",
		SourceType:       "issue_comment",
		GitHubCommentID:  id,
		SourceURL:        "https://github.com/owner/repo/issues/683#issuecomment-1",
		AuthorLogin:      "maintainer",
		AuthorAuthorized: true,
		IssueNumber:      683,
		RawBody:          "tighten catalog guidance /agents improve",
		Tag:              store.FeedbackTag,
		LinkConfidence:   "exact",
	})
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	return feedback
}

func TestRecommendationLifecycleOwnedByService(t *testing.T) {
	t.Parallel()
	svc, st, _ := newServiceTest(t)
	feedback := seedFeedback(t, st, "team-a", 683001)

	rec, err := svc.RecordRecommendation(RecommendationFromFeedback(feedback))
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}
	if rec.Status != RecommendationStatusNeedsUserInput || rec.Type != "needs_more_context" {
		t.Fatalf("recommendation = (%q, %q), want needs_user_input needs_more_context", rec.Status, rec.Type)
	}
	rows, err := st.ListSelfImprovementFeedback("team-a", store.FeedbackStatusAnalyzed, 10)
	if err != nil {
		t.Fatalf("ListSelfImprovementFeedback: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != feedback.ID {
		t.Fatalf("analyzed feedback = %+v, want feedback %d", rows, feedback.ID)
	}

	clarified, err := svc.UpsertClarification(rec.ID, "dashboard", "Apply this only to reviewer guidance.")
	if err != nil {
		t.Fatalf("UpsertClarification: %v", err)
	}
	if clarified.Status != RecommendationStatusClarifying {
		t.Fatalf("clarified status = %q, want clarifying", clarified.Status)
	}
	if clarified.Clarification == nil || clarified.Clarification.Body != "Apply this only to reviewer guidance." {
		t.Fatalf("clarification = %+v, want stored body", clarified.Clarification)
	}

	if _, err := svc.UpdateRecommendationStatus(rec.ID, "unknown", ""); err == nil {
		t.Fatal("UpdateRecommendationStatus unknown status succeeded, want validation error")
	}
	if _, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusFailed, "backend failed"); err == nil {
		t.Fatal("UpdateRecommendationStatus failed succeeded, want validation error")
	}
	if _, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusRejected, ""); err == nil {
		t.Fatal("UpdateRecommendationStatus rejected while clarifying succeeded, want validation error")
	}

	rejectFeedback := seedFeedback(t, st, "team-a", 683101)
	rejectRec, err := svc.RecordRecommendation(RecommendationFromFeedback(rejectFeedback))
	if err != nil {
		t.Fatalf("RecordRecommendation reject target: %v", err)
	}
	rejected, err := svc.UpdateRecommendationStatus(rejectRec.ID, RecommendationStatusRejected, "Not useful")
	if err != nil {
		t.Fatalf("UpdateRecommendationStatus rejected: %v", err)
	}
	if rejected.Status != RecommendationStatusRejected {
		t.Fatalf("rejected status = %q, want rejected", rejected.Status)
	}
	if rejected.DecisionReason != "Not useful" {
		t.Fatalf("rejected decision reason = %q, want stored reason", rejected.DecisionReason)
	}
	rejectedAgain, err := svc.UpdateRecommendationStatus(rejectRec.ID, RecommendationStatusRejected, "")
	if err != nil {
		t.Fatalf("UpdateRecommendationStatus rejected again: %v", err)
	}
	if rejectedAgain.Status != RecommendationStatusRejected {
		t.Fatalf("rejected again status = %q, want rejected", rejectedAgain.Status)
	}
	if _, err := svc.UpsertClarification(rejectRec.ID, "dashboard", "Re-open this decision."); err == nil {
		t.Fatal("UpsertClarification on terminal recommendation succeeded, want validation error")
	}

	failedFeedback := seedFeedback(t, st, "team-a", 683201)
	failedRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           failedFeedback.WorkspaceID,
		FeedbackEventID:       failedFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusFailed,
		Confidence:            "low",
		Risk:                  "medium",
		Finding:               "Analyzer failed",
		NormalizedLesson:      "Retry clarification",
		Rationale:             "The clarification run failed before producing a proposal.",
		AttributionConfidence: "unresolved",
		Error:                 "runner container exited with status 1",
	})
	if err != nil {
		t.Fatalf("RecordRecommendation failed: %v", err)
	}
	retried, err := svc.UpsertClarification(failedRec.ID, "dashboard", "Retry with the same clarification.")
	if err != nil {
		t.Fatalf("UpsertClarification on failed recommendation: %v", err)
	}
	if retried.Status != RecommendationStatusClarifying {
		t.Fatalf("retried failed status = %q, want clarifying", retried.Status)
	}
}

func TestProposalBundleBehaviorOwnedByService(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "bundle-prompt", Description: "desc", Content: "prompt v1"})
	if err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if err := store.UpsertSkill(db, "existing-skill", fleet.Skill{Name: "existing-skill", Prompt: "skill v1"}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	feedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683002)
	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       feedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "coordinated catalog update",
		Rationale:             "prompt update plus a duplicate skill proposal",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationUpdateExisting, "asset_type": "prompt", "asset_id": prompt.ID, "base_version_id": prompt.VersionID, "proposed_body": "prompt v2"},
				{"operation": ProposalBundleOperationCreateNew, "asset_type": "skill", "proposed_ref": "skill_duplicate", "proposed_name": "duplicate", "proposed_scope": "workspace", "proposed_body": "duplicate skill"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}

	if rec.ProposalBundle == nil {
		t.Fatalf("RecordRecommendation did not attach an automatic proposal bundle")
	}
	bundle := *rec.ProposalBundle
	if bundle.Status != ProposalBundleStatusPending || len(bundle.Items) != 2 {
		t.Fatalf("bundle = %+v, want two pending items", bundle)
	}
	duplicateFeedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683112)
	duplicateRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       duplicateFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "duplicate catalog update",
		Rationale:             "same prompt should already have one open draft",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationUpdateExisting, "asset_type": "prompt", "asset_id": prompt.ID, "base_version_id": prompt.VersionID, "proposed_body": "prompt v2 duplicate"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation duplicate: %v", err)
	}
	if duplicateRec.Status != RecommendationStatusNeedsUserInput || duplicateRec.ProposalBundle != nil || !strings.Contains(duplicateRec.Error, "already has an open proposal draft") {
		t.Fatalf("duplicate recommendation = %+v, want needs_user_input conflict without bundle", duplicateRec)
	}
	var promptItem, skillItem SelfImprovementBundleItem
	for _, item := range bundle.Items {
		switch item.AssetType {
		case "prompt":
			promptItem = item
		case "skill":
			skillItem = item
		}
	}
	if _, err := svc.UpdateProposalBundleItem(bundle.ID, promptItem.ID, SelfImprovementBundleItemUpdate{ProposedBody: "prompt v2 edited"}, "system"); err != nil {
		t.Fatalf("UpdateProposalBundleItem: %v", err)
	}
	if _, err := svc.LinkProposalBundleItem(bundle.ID, skillItem.ID, "existing-skill", "already covered", "system"); err != nil {
		t.Fatalf("LinkProposalBundleItem: %v", err)
	}
	published, err := svc.PublishProposalBundle(bundle.ID, "system")
	if err != nil {
		t.Fatalf("PublishProposalBundle: %v", err)
	}
	if published.Status != ProposalBundleStatusPublished {
		t.Fatalf("published status = %q, want published", published.Status)
	}
	for _, item := range published.Items {
		if item.AssetType == "skill" && item.Decision != ProposalBundleDecisionLinkedExisting {
			t.Fatalf("linked skill decision = %q, want linked_existing", item.Decision)
		}
		if item.AssetType == "prompt" && item.PublishedVersionID == "" {
			t.Fatalf("prompt published version is empty")
		}
	}
	_, err = svc.DiscardProposalBundle(bundle.ID, "system")
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("DiscardProposalBundle after publish err = %v, want validation", err)
	}

	resolveFeedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683152)
	resolveRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       resolveFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "new skill duplicates existing guidance",
		Rationale:             "existing skill should be reused",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationCreateNew, "asset_type": "skill", "proposed_ref": "existing-skill-copy", "proposed_name": "Existing skill copy", "proposed_scope": "workspace", "proposed_body": "duplicate skill"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation resolve-only: %v", err)
	}
	if resolveRec.ProposalBundle == nil || len(resolveRec.ProposalBundle.Items) != 1 {
		t.Fatalf("resolve-only bundle = %+v, want one item", resolveRec.ProposalBundle)
	}
	resolveBundle, err := svc.LinkProposalBundleItem(resolveRec.ProposalBundle.ID, resolveRec.ProposalBundle.Items[0].ID, "existing-skill", "already covered", "system")
	if err != nil {
		t.Fatalf("LinkProposalBundleItem resolve-only: %v", err)
	}
	resolved, err := svc.PublishProposalBundle(resolveBundle.ID, "system")
	if err != nil {
		t.Fatalf("PublishProposalBundle resolve-only: %v", err)
	}
	if resolved.Status != ProposalBundleStatusResolved {
		t.Fatalf("resolved status = %q, want resolved", resolved.Status)
	}

	discardPrompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "discard-bundle-prompt", Description: "desc", Content: "discard v1"})
	if err != nil {
		t.Fatalf("seed discard prompt: %v", err)
	}
	discardFeedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683202)
	discardRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       discardFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "discard catalog update",
		Rationale:             "prompt update should not proceed",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationUpdateExisting, "asset_type": "prompt", "asset_id": discardPrompt.ID, "base_version_id": discardPrompt.VersionID, "proposed_body": "discard v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation discard: %v", err)
	}
	if discardRec.ProposalBundle == nil {
		t.Fatalf("RecordRecommendation discard did not attach an automatic proposal bundle")
	}
	discardBundle := *discardRec.ProposalBundle
	if _, err := svc.DiscardProposalBundle(discardBundle.ID, "system"); err != nil {
		t.Fatalf("DiscardProposalBundle: %v", err)
	}

	rejectPrompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "reject-single-item-prompt", Description: "desc", Content: "reject v1"})
	if err != nil {
		t.Fatalf("seed reject prompt: %v", err)
	}
	rejectFeedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683303)
	rejectRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       rejectFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "single item reject",
		Rationale:             "prompt update should be rejected",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationUpdateExisting, "asset_type": "prompt", "asset_id": rejectPrompt.ID, "base_version_id": rejectPrompt.VersionID, "proposed_body": "reject v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation single-item reject: %v", err)
	}
	if rejectRec.ProposalBundle == nil || len(rejectRec.ProposalBundle.Items) != 1 {
		t.Fatalf("reject proposal bundle = %+v, want one item", rejectRec.ProposalBundle)
	}
	rejectedBundle, err := svc.RejectProposalBundleItem(rejectRec.ProposalBundle.ID, rejectRec.ProposalBundle.Items[0].ID, "wrong target", "system")
	if err != nil {
		t.Fatalf("RejectProposalBundleItem: %v", err)
	}
	if rejectedBundle.Status != ProposalBundleStatusDiscarded {
		t.Fatalf("rejected bundle status = %q, want discarded", rejectedBundle.Status)
	}
	rejectedRec, err := svc.GetRecommendation(rejectRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation rejected: %v", err)
	}
	if rejectedRec.Status != RecommendationStatusRejected || rejectedRec.DecisionReason != "wrong target" {
		t.Fatalf("rejected recommendation = %+v, want rejected with reason", rejectedRec)
	}
}

func TestProposalBundleNoopEditDoesNotRecordEditEvent(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "noop-bundle-prompt", Description: "desc", Content: "prompt v1"})
	if err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	feedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683203)
	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       feedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "catalog update",
		Rationale:             "prompt update should be proposed",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationUpdateExisting, "asset_type": "prompt", "asset_id": prompt.ID, "base_version_id": prompt.VersionID, "proposed_body": "prompt v2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}
	if rec.ProposalBundle == nil {
		t.Fatalf("RecordRecommendation did not attach an automatic proposal bundle")
	}
	bundle := *rec.ProposalBundle
	if len(bundle.Items) != 1 {
		t.Fatalf("bundle items = %d, want 1", len(bundle.Items))
	}
	item := bundle.Items[0]
	if _, err := svc.UpdateProposalBundleItem(bundle.ID, item.ID, SelfImprovementBundleItemUpdate{ProposedBody: item.ProposedBody}, "system"); err != nil {
		t.Fatalf("UpdateProposalBundleItem no-op: %v", err)
	}

	var editEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM self_improvement_proposal_bundle_item_events WHERE bundle_id=? AND item_id=? AND event_type='edited'`, bundle.ID, item.ID).Scan(&editEvents); err != nil {
		t.Fatalf("count edit events: %v", err)
	}
	if editEvents != 0 {
		t.Fatalf("edit events = %d, want 0", editEvents)
	}
}

func TestRecommendedPatchOnlyRecommendationNeedsInputInsteadOfDeadEnd(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "patch-only-prompt", Description: "desc", Content: "prompt v1"})
	if err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	feedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683204)

	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:             fleet.DefaultWorkspaceID,
		FeedbackEventID:         feedback.ID,
		Type:                    "catalog_patch",
		Status:                  RecommendationStatusRecommended,
		Finding:                 "patch-only catalog update",
		Rationale:               "analyst returned prose patch without editable body",
		AttributionConfidence:   "exact",
		TargetAssetType:         "prompt",
		TargetAssetID:           prompt.ID,
		TargetBaseVersionID:     prompt.VersionID,
		ProposedPatch:           "Append a new section.",
		AnalyzerPromptRef:       "prompt_self-improvement-analyst",
		AnalyzerPromptVersionID: "promptver_self_improvement_analyst_v6",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}
	if rec.Status != RecommendationStatusNeedsUserInput {
		t.Fatalf("status = %q, want needs_user_input", rec.Status)
	}
	if rec.ProposalBundle != nil {
		t.Fatalf("proposal bundle = %+v, want none for patch-only output", rec.ProposalBundle)
	}
	if rec.Error == "" {
		t.Fatal("error is empty, want bundle creation reason")
	}
}
