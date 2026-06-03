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

	_, err = svc.DiscardProposalBundle(bundle.ID, "system")
	var validation *store.ErrValidation
	if !errors.As(err, &validation) {
		t.Fatalf("DiscardProposalBundle after publish err = %v, want validation", err)
	}
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
	missingBase, err := svc.RecordRecommendation(SelfImprovementRecommendationInput{
		WorkspaceID:           "team-a",
		FeedbackEventID:       feedback.ID,
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
