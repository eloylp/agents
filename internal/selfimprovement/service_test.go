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
	if clarified.Status != RecommendationStatusNeedsUserInput {
		t.Fatalf("clarified status = %q, want needs_user_input", clarified.Status)
	}
	if clarified.Clarification == nil || clarified.Clarification.Body != "Apply this only to reviewer guidance." {
		t.Fatalf("clarification = %+v, want stored body", clarified.Clarification)
	}

	accepted, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusAccepted)
	if err != nil {
		t.Fatalf("UpdateRecommendationStatus: %v", err)
	}
	if accepted.Status != RecommendationStatusAccepted {
		t.Fatalf("accepted status = %q, want accepted", accepted.Status)
	}
	if _, err := svc.UpdateRecommendationStatus(rec.ID, "unknown"); err == nil {
		t.Fatal("UpdateRecommendationStatus unknown status succeeded, want validation error")
	}
	if _, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusRejected); err == nil {
		t.Fatal("UpdateRecommendationStatus changed accepted recommendation, want validation error")
	}
	if _, err := svc.UpsertClarification(rec.ID, "dashboard", "Re-open this decision."); err == nil {
		t.Fatal("UpsertClarification on terminal recommendation succeeded, want validation error")
	}
}

func TestAssistantMemoryLifecycleAndRecommendationInfluence(t *testing.T) {
	t.Parallel()
	svc, st, _ := newServiceTest(t)
	feedback := seedFeedback(t, st, "team-a", 664001)

	proposed, err := svc.CreateMemory(AssistantMemoryInput{
		WorkspaceID:  "team-a",
		Key:          "prefer_skills",
		Value:        "Prefer reusable skills for guidance shared by multiple agents.",
		Status:       MemoryStatusProposed,
		EvidenceType: "manual_user_entry",
		Confidence:   "medium",
	})
	if err != nil {
		t.Fatalf("CreateMemory proposed: %v", err)
	}
	if proposed.Status != MemoryStatusProposed {
		t.Fatalf("proposed status = %q, want proposed", proposed.Status)
	}
	active, err := svc.ApproveMemory(proposed.ID)
	if err != nil {
		t.Fatalf("ApproveMemory: %v", err)
	}
	if active.Status != MemoryStatusActive || active.ApprovedAt == "" {
		t.Fatalf("approved memory = %+v, want active with approved_at", active)
	}
	otherWorkspaceMemory, err := svc.ListActiveMemory("team-b", 20)
	if err != nil {
		t.Fatalf("ListActiveMemory other workspace: %v", err)
	}
	if len(otherWorkspaceMemory) != 1 || otherWorkspaceMemory[0].ID != active.ID {
		t.Fatalf("other workspace memory = %+v, want global active memory %s", otherWorkspaceMemory, active.ID)
	}
	updatedValue := "Prefer reusable skills over longer prompts when guidance is shared."
	updated, err := svc.UpdateMemory(active.ID, AssistantMemoryUpdate{Value: &updatedValue})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Value != updatedValue {
		t.Fatalf("updated value = %q, want %q", updated.Value, updatedValue)
	}

	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedback.ID,
		Type:                  "skill_guidance",
		Status:                RecommendationStatusRecommended,
		Finding:               "Extract shared guidance.",
		Rationale:             "The feedback applies to multiple agents.",
		AttributionConfidence: "exact",
		StructuredOutput:      map[string]any{"memory_influence_ids": []string{active.ID}},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}
	if len(rec.MemoryInfluences) != 1 || rec.MemoryInfluences[0].ID != active.ID {
		t.Fatalf("memory influences = %+v, want approved memory %s", rec.MemoryInfluences, active.ID)
	}
	feedbackWithoutInfluence := seedFeedback(t, st, "team-a", 664002)
	withoutInfluence, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedbackWithoutInfluence.ID,
		Type:                  "skill_guidance",
		Status:                RecommendationStatusRecommended,
		Finding:               "Extract shared guidance without stored memory.",
		Rationale:             "The current feedback was sufficient.",
		AttributionConfidence: "exact",
		StructuredOutput:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("RecordRecommendation without influence: %v", err)
	}
	if len(withoutInfluence.MemoryInfluences) != 0 {
		t.Fatalf("memory influences without ids = %+v, want none", withoutInfluence.MemoryInfluences)
	}

	if _, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusRejected); err != nil {
		t.Fatalf("UpdateRecommendationStatus rejected: %v", err)
	}
	memories, err := svc.ListMemory("team-a", MemoryStatusProposed, 20)
	if err != nil {
		t.Fatalf("ListMemory proposed: %v", err)
	}
	if len(memories) != 1 || memories[0].EvidenceType != "rejected_recommendation" || memories[0].EvidenceID != rec.ID {
		t.Fatalf("proposed decision memory = %+v, want rejected recommendation evidence", memories)
	}
	rejected, err := svc.RejectMemory(memories[0].ID, "Too broad")
	if err != nil {
		t.Fatalf("RejectMemory: %v", err)
	}
	if rejected.Status != MemoryStatusRejected || rejected.RejectedReason != "Too broad" {
		t.Fatalf("rejected memory = %+v, want rejected with reason", rejected)
	}
	archived, err := svc.ArchiveMemory(active.ID)
	if err != nil {
		t.Fatalf("ArchiveMemory: %v", err)
	}
	if archived.Status != MemoryStatusArchived || archived.ArchivedAt == "" {
		t.Fatalf("archived memory = %+v, want archived with archived_at", archived)
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
		Status:                RecommendationStatusAccepted,
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

	bundle, err := svc.CreateProposalBundle(rec.ID)
	if err != nil {
		t.Fatalf("CreateProposalBundle: %v", err)
	}
	if bundle.Status != ProposalBundleStatusPending || len(bundle.Items) != 2 {
		t.Fatalf("bundle = %+v, want two pending items", bundle)
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
	memories, err := svc.ListMemory(fleet.DefaultWorkspaceID, MemoryStatusProposed, 20)
	if err != nil {
		t.Fatalf("ListMemory proposed: %v", err)
	}
	wantEvidenceTypes := []string{"edited_proposal_bundle_item", "linked_existing_proposal_bundle_item", "published_proposal_bundle"}
	for _, evidenceType := range wantEvidenceTypes {
		if !hasMemoryEvidenceType(memories, evidenceType) {
			t.Fatalf("proposed memory evidence types = %+v, want %q", memories, evidenceType)
		}
	}

	_, err = svc.DiscardProposalBundle(bundle.ID, "system")
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("DiscardProposalBundle after publish err = %v, want validation", err)
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
		Status:                RecommendationStatusAccepted,
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
	discardBundle, err := svc.CreateProposalBundle(discardRec.ID)
	if err != nil {
		t.Fatalf("CreateProposalBundle discard: %v", err)
	}
	if _, err := svc.DiscardProposalBundle(discardBundle.ID, "system"); err != nil {
		t.Fatalf("DiscardProposalBundle: %v", err)
	}
	memories, err = svc.ListMemory(fleet.DefaultWorkspaceID, MemoryStatusProposed, 20)
	if err != nil {
		t.Fatalf("ListMemory proposed after discard: %v", err)
	}
	if !hasMemoryEvidenceType(memories, "discarded_proposal_bundle") {
		t.Fatalf("proposed memory evidence types after discard = %+v, want discarded_proposal_bundle", memories)
	}
}

func TestProposalBundleNoopEditDoesNotProposeMemory(t *testing.T) {
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
		Status:                RecommendationStatusAccepted,
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
	bundle, err := svc.CreateProposalBundle(rec.ID)
	if err != nil {
		t.Fatalf("CreateProposalBundle: %v", err)
	}
	if len(bundle.Items) != 1 {
		t.Fatalf("bundle items = %d, want 1", len(bundle.Items))
	}
	item := bundle.Items[0]
	if _, err := svc.UpdateProposalBundleItem(bundle.ID, item.ID, SelfImprovementBundleItemUpdate{ProposedBody: item.ProposedBody}, "system"); err != nil {
		t.Fatalf("UpdateProposalBundleItem no-op: %v", err)
	}

	memories, err := svc.ListMemory(fleet.DefaultWorkspaceID, MemoryStatusProposed, 20)
	if err != nil {
		t.Fatalf("ListMemory proposed: %v", err)
	}
	if hasMemoryEvidenceType(memories, "edited_proposal_bundle_item") {
		t.Fatalf("proposed memory evidence types = %+v, want no edited_proposal_bundle_item", memories)
	}
	var editEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM self_improvement_proposal_bundle_item_events WHERE bundle_id=? AND item_id=? AND event_type='edited'`, bundle.ID, item.ID).Scan(&editEvents); err != nil {
		t.Fatalf("count edit events: %v", err)
	}
	if editEvents != 0 {
		t.Fatalf("edit events = %d, want 0", editEvents)
	}
}

func hasMemoryEvidenceType(memories []AssistantMemory, evidenceType string) bool {
	for _, memory := range memories {
		if memory.EvidenceType == evidenceType {
			return true
		}
	}
	return false
}

func TestCreateProposalFromAcceptedRecommendation(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "proposal-target", Description: "target desc", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt: %v", err)
	}
	feedback := seedFeedback(t, st, "team-a", 683003)
	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedback.ID,
		Type:                  "prompt_guidance",
		Status:                RecommendationStatusAccepted,
		Finding:               "tighten prompt guidance",
		NormalizedLesson:      "Keep guidance concrete.",
		Rationale:             "Feedback asked for a concrete prompt update.",
		TargetAssetType:       "prompt",
		TargetAssetID:         prompt.ID,
		TargetBaseVersionID:   prompt.VersionID,
		ProposedNewBody:       "body v2",
		AnalyzerPromptRef:     "prompt_self-improvement-analyst",
		StructuredOutput:      map[string]any{"status": "recommended"},
		AttributionConfidence: "exact",
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}

	proposal, err := svc.CreateProposal(rec.ID)
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if proposal.Version.State != "proposal" || proposal.Version.SourceType != "feedback_recommendation" ||
		proposal.Version.SourceRef != rec.ID || proposal.Version.Author != "agents-assistant" {
		t.Fatalf("proposal metadata = %+v, want inert feedback recommendation proposal", proposal.Version)
	}
	if proposal.Version.BaseVersionID != prompt.VersionID {
		t.Fatalf("proposal base = %q, want %q", proposal.Version.BaseVersionID, prompt.VersionID)
	}
	listed, err := svc.ListProposals(rec.ID)
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("proposals len = %d, want 1", len(listed))
	}
	if listed[0].BaseVersion == nil || listed[0].BaseVersion.ID != prompt.VersionID || listed[0].BaseVersion.Content != "body v1" {
		t.Fatalf("proposal base version = %+v, want prompt version %q with body v1", listed[0].BaseVersion, prompt.VersionID)
	}
	if listed[0].Version.Content != "body v2" {
		t.Fatalf("proposal version content = %q, want body v2", listed[0].Version.Content)
	}
	current, err := store.ReadPrompt(db, prompt.ID)
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if current.Content != "body v1" || current.VersionID != prompt.VersionID {
		t.Fatalf("current prompt = id %q body %q, want published version unchanged", current.VersionID, current.Content)
	}
	second, err := svc.CreateProposal(rec.ID)
	if err != nil {
		t.Fatalf("CreateProposal second call: %v", err)
	}
	if second.Version.ID != proposal.Version.ID {
		t.Fatalf("second proposal id = %q, want existing %q", second.Version.ID, proposal.Version.ID)
	}
	linked, err := svc.ListRecommendationsWithProposals("team-a", 10)
	if err != nil {
		t.Fatalf("ListRecommendationsWithProposals: %v", err)
	}
	if len(linked) != 1 || linked[0].ID != rec.ID {
		t.Fatalf("linked recommendations = %+v, want %s", linked, rec.ID)
	}
}

func TestCreateProposalRejectsUnsafeStatesAndTargets(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	feedback := seedFeedback(t, st, "team-a", 683004)
	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedback.ID,
		Type:                  "needs_more_context",
		Status:                RecommendationStatusRecommended,
		Finding:               "not accepted",
		TargetAssetType:       "prompt",
		TargetAssetID:         "prompt_missing",
		TargetBaseVersionID:   "promptver_missing",
		ProposedNewBody:       "body",
		AttributionConfidence: "unresolved",
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}
	if _, err := svc.CreateProposal(rec.ID); err == nil || !strings.Contains(err.Error(), "must be accepted") {
		t.Fatalf("CreateProposal error = %v, want accepted-state validation", err)
	}
	accepted, err := svc.UpdateRecommendationStatus(rec.ID, RecommendationStatusAccepted)
	if err != nil {
		t.Fatalf("UpdateRecommendationStatus: %v", err)
	}
	if _, err := svc.CreateProposal(accepted.ID); err == nil || !strings.Contains(err.Error(), "not proposal-convertible") {
		t.Fatalf("CreateProposal error = %v, want non-convertible validation", err)
	}

	prompt, err := store.UpsertPrompt(db, fleet.Prompt{Name: "missing-base-proposal-target", Description: "target desc", Content: "body v1"})
	if err != nil {
		t.Fatalf("UpsertPrompt: %v", err)
	}
	missingBaseFeedback := seedFeedback(t, st, "team-a", 683104)
	missingBase, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       missingBaseFeedback.ID,
		Type:                  "prompt_guidance",
		Status:                RecommendationStatusAccepted,
		Finding:               "tighten prompt guidance",
		TargetAssetType:       "prompt",
		TargetAssetID:         prompt.ID,
		ProposedNewBody:       "body v2",
		AttributionConfidence: "exact",
	})
	if err != nil {
		t.Fatalf("RecordRecommendation missing base: %v", err)
	}
	if _, err := svc.CreateProposal(missingBase.ID); err == nil || !strings.Contains(err.Error(), "base version is required") {
		t.Fatalf("CreateProposal error = %v, want missing base version validation", err)
	}
}

func TestCreateProposalListsGuardrailMetadata(t *testing.T) {
	t.Parallel()
	svc, st, db := newServiceTest(t)
	if _, err := store.UpsertWorkspace(db, fleet.Workspace{ID: "team-a", Name: "Team A"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := store.UpsertGuardrail(db, fleet.Guardrail{
		Name:        "proposal-guardrail-metadata",
		Description: "guardrail desc",
		Content:     "guardrail v1",
		Enabled:     true,
		Position:    11,
	}); err != nil {
		t.Fatalf("UpsertGuardrail: %v", err)
	}
	guardrail, err := store.GetGuardrail(db, "proposal-guardrail-metadata")
	if err != nil {
		t.Fatalf("GetGuardrail: %v", err)
	}
	feedback := seedFeedback(t, st, "team-a", 683005)
	rec, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedback.ID,
		Type:                  "guardrail_guidance",
		Status:                RecommendationStatusAccepted,
		Finding:               "tighten guardrail guidance",
		TargetAssetType:       "guardrail",
		TargetAssetID:         guardrail.ID,
		TargetBaseVersionID:   guardrail.VersionID,
		ProposedNewBody:       "guardrail v2",
		AttributionConfidence: "exact",
	})
	if err != nil {
		t.Fatalf("RecordRecommendation: %v", err)
	}

	if _, err := svc.CreateProposal(rec.ID); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	listed, err := svc.ListProposals(rec.ID)
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("proposals len = %d, want 1", len(listed))
	}
	if !listed[0].Version.Enabled || listed[0].Version.Position != 11 {
		t.Fatalf("proposal guardrail metadata = enabled %v position %d, want enabled true position 11", listed[0].Version.Enabled, listed[0].Version.Position)
	}
	if listed[0].BaseVersion == nil || !listed[0].BaseVersion.Enabled || listed[0].BaseVersion.Position != 11 {
		t.Fatalf("base guardrail metadata = %+v, want enabled true position 11", listed[0].BaseVersion)
	}
}
