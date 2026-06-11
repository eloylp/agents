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

func recommendationInputForFeedback(feedback store.SelfImprovementFeedback) SelfImprovementRecommendationInput {
	finding := firstFeedbackLine(feedback.RawBody)
	if finding == "" {
		finding = "Review the stored feedback evidence and decide whether a catalog change is warranted."
	}
	return SelfImprovementRecommendationInput{
		WorkspaceID:           feedback.WorkspaceID,
		FeedbackEventID:       feedback.ID,
		Type:                  "needs_more_context",
		Status:                RecommendationStatusNeedsUserInput,
		Confidence:            "low",
		Risk:                  "low",
		Finding:               finding,
		NormalizedLesson:      normalizeLesson(finding),
		Rationale:             "The recommendation is review-only and does not publish or mutate catalog assets.",
		EvidenceFeedbackIDs:   []int64{feedback.ID},
		EvidenceSourceURLs:    []string{feedback.SourceURL},
		AttributionConfidence: feedback.LinkConfidence,
		AnalyzerPromptRef:     "prompt_self-improvement-analyst",
		StructuredOutput: map[string]any{
			"type":                    "needs_more_context",
			"status":                  RecommendationStatusNeedsUserInput,
			"confidence":              "low",
			"risk":                    "low",
			"finding":                 finding,
			"normalized_lesson":       normalizeLesson(finding),
			"rationale":               "The recommendation is review-only and does not publish or mutate catalog assets.",
			"evidence_feedback_ids":   []int64{feedback.ID},
			"evidence_source_urls":    []string{feedback.SourceURL},
			"attribution_confidence":  feedback.LinkConfidence,
			"target_asset_type":       "",
			"target_asset_id":         "",
			"target_base_version_id":  "",
			"proposed_patch":          "",
			"proposed_new_body":       "",
			"changes":                 []map[string]any{},
			"no_auto_apply_confirmed": true,
		},
	}
}

func TestRecommendationLifecycleOwnedByService(t *testing.T) {
	t.Parallel()
	svc, st, _ := newServiceTest(t)
	feedback := seedFeedback(t, st, "team-a", 683001)

	queuedFeedback := seedFeedback(t, st, "team-a", 683000)
	queued, previousStatus, err := svc.BeginAnalysis(queuedFeedback)
	if err != nil {
		t.Fatalf("BeginAnalysis: %v", err)
	}
	if previousStatus != "" || queued.Status != RecommendationStatusAnalyzing || queued.ID == "" {
		t.Fatalf("queued recommendation = %+v previous=%q, want new analyzing row", queued, previousStatus)
	}
	if queued.Feedback == nil || queued.Feedback.ID != queuedFeedback.ID {
		t.Fatalf("queued feedback = %+v, want feedback %d", queued.Feedback, queuedFeedback.ID)
	}
	storedQueued, err := st.GetSelfImprovementRecommendation(queued.ID)
	if err != nil {
		t.Fatalf("get queued recommendation: %v", err)
	}
	if storedQueued.Status != RecommendationStatusAnalyzing {
		t.Fatalf("stored queued status = %q, want analyzing", storedQueued.Status)
	}

	rec, err := svc.RecordRecommendation(recommendationInputForFeedback(feedback))
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
	rejectRec, err := svc.RecordRecommendation(recommendationInputForFeedback(rejectFeedback))
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
	if _, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusRejected, "too late"); err == nil {
		t.Fatal("UpdateRecommendationStatus after publish succeeded, want validation error")
	}
	if _, err := svc.UpsertClarification(rec.ID, "dashboard", "Re-open published bundle."); err == nil {
		t.Fatal("UpsertClarification after publish succeeded, want validation error")
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
	if _, err := svc.UpdateRecommendationStatus(discardRec.ID, RecommendationStatusRejected, "too late"); err == nil {
		t.Fatal("UpdateRecommendationStatus after discard succeeded, want validation error")
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

func TestProposalBundleRejectsStaleRecommendationSnapshotAndGuardrailLink(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "changed-source-prompt", Description: "desc", Content: "prompt v1"})
	if err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	feedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683502)
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
		t.Fatal("RecordRecommendation did not attach proposal bundle")
	}
	if _, err := db.Exec(`UPDATE self_improvement_recommendations SET finding='changed after bundle creation', updated_at=datetime('now', '+1 second') WHERE id=?`, rec.ID); err != nil {
		t.Fatalf("mutate recommendation: %v", err)
	}
	if _, err := svc.PublishProposalBundle(rec.ProposalBundle.ID, "system"); err == nil {
		t.Fatal("PublishProposalBundle after source recommendation changed succeeded, want validation error")
	}

	if err := store.UpsertGuardrail(db, fleet.Guardrail{Name: "existing-guardrail", Description: "desc", Content: "body", Enabled: true, Position: 10}); err != nil {
		t.Fatalf("seed guardrail: %v", err)
	}
	guardFeedback := seedFeedback(t, st, fleet.DefaultWorkspaceID, 683503)
	guardRec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           fleet.DefaultWorkspaceID,
		FeedbackEventID:       guardFeedback.ID,
		Type:                  "catalog_patch_bundle",
		Status:                RecommendationStatusRecommended,
		Finding:               "new guardrail",
		Rationale:             "guardrail should be created",
		AttributionConfidence: "exact",
		StructuredOutput: map[string]any{
			"changes": []map[string]any{
				{"operation": ProposalBundleOperationCreateNew, "asset_type": "guardrail", "proposed_ref": "new-guardrail", "proposed_name": "new guardrail", "proposed_scope": "global", "proposed_body": "guardrail body"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation guardrail: %v", err)
	}
	if guardRec.ProposalBundle == nil || len(guardRec.ProposalBundle.Items) != 1 {
		t.Fatalf("guardrail bundle = %+v, want one item", guardRec.ProposalBundle)
	}
	if _, err := svc.LinkProposalBundleItem(guardRec.ProposalBundle.ID, guardRec.ProposalBundle.Items[0].ID, "existing-guardrail", "already exists", "system"); err == nil {
		t.Fatal("LinkProposalBundleItem guardrail succeeded, want validation error")
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
