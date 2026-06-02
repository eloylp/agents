package selfimprovement

import (
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/store"
)

const (
	RecommendationStatusRecommended    = "recommended"
	RecommendationStatusNeedsUserInput = "needs_user_input"
	RecommendationStatusAccepted       = "accepted"
	RecommendationStatusRejected       = "rejected"
	RecommendationStatusDeferred       = "deferred"
	RecommendationStatusDuplicate      = "duplicate"
	RecommendationStatusFailed         = "failed"
)

type SelfImprovementRecommendationInput = store.SelfImprovementRecommendationInput

func RecommendationFromFeedback(feedback store.SelfImprovementFeedback) SelfImprovementRecommendationInput {
	finding := firstFeedbackLine(feedback.RawBody)
	if finding == "" {
		finding = "Review the stored feedback evidence and decide whether a catalog change is warranted."
	}
	recType := "needs_more_context"
	status := RecommendationStatusNeedsUserInput
	confidence := "low"
	if feedback.LinkedPromptVersionID != "" || len(feedback.LinkedSkillVersionIDs) > 0 || len(feedback.LinkedGuardrailVersionIDs) > 0 {
		recType = "deduplicate_guidance"
		status = RecommendationStatusRecommended
		confidence = "medium"
	}
	targetType, targetID, targetVersion := recommendationTarget(feedback)
	rationale := fmt.Sprintf("Feedback event %d was captured from %s with %s attribution. The recommendation is review-only and does not publish or mutate catalog assets.", feedback.ID, feedback.SourceURL, feedback.LinkConfidence)
	lesson := normalizeLesson(finding)
	structured := map[string]any{
		"type":                    recType,
		"status":                  status,
		"confidence":              confidence,
		"risk":                    "low",
		"finding":                 finding,
		"normalized_lesson":       lesson,
		"rationale":               rationale,
		"evidence_feedback_ids":   []int64{feedback.ID},
		"evidence_source_urls":    []string{feedback.SourceURL},
		"attribution_confidence":  feedback.LinkConfidence,
		"target_asset_type":       targetType,
		"target_asset_id":         targetID,
		"target_base_version_id":  targetVersion,
		"proposed_patch":          "",
		"proposed_new_body":       "",
		"suggested_rollout_scope": "workspace",
		"analyzer_prompt_ref":     "prompt_self-improvement-analyst",
		"no_auto_apply_confirmed": true,
	}
	return SelfImprovementRecommendationInput{
		WorkspaceID:           feedback.WorkspaceID,
		FeedbackEventID:       feedback.ID,
		Type:                  recType,
		Status:                status,
		Confidence:            confidence,
		Risk:                  "low",
		Finding:               finding,
		NormalizedLesson:      lesson,
		Rationale:             rationale,
		EvidenceFeedbackIDs:   []int64{feedback.ID},
		EvidenceSourceURLs:    []string{feedback.SourceURL},
		AttributionConfidence: feedback.LinkConfidence,
		TargetAssetType:       targetType,
		TargetAssetID:         targetID,
		TargetBaseVersionID:   targetVersion,
		SuggestedRolloutScope: "workspace",
		AnalyzerPromptRef:     "prompt_self-improvement-analyst",
		StructuredOutput:      structured,
	}
}

func (s *Service) RecordRecommendation(in SelfImprovementRecommendationInput) (SelfImprovementRecommendation, error) {
	in.Type = strings.TrimSpace(in.Type)
	if in.Type == "" {
		in.Type = "needs_more_context"
	}
	if in.Status == "" {
		in.Status = RecommendationStatusRecommended
	}
	if !validRecommendationStatus(in.Status) {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("unsupported recommendation status %q", in.Status)}
	}
	in.Confidence = defaultString(in.Confidence, "low")
	in.Risk = defaultString(in.Risk, "low")
	in.AttributionConfidence = defaultString(in.AttributionConfidence, "unresolved")
	in.AnalyzerPromptRef = defaultString(in.AnalyzerPromptRef, "prompt_self-improvement-analyst")
	if err := s.store.Transact(func(tx *store.Tx) error {
		if err := store.UpsertSelfImprovementRecommendationRow(tx, in); err != nil {
			return err
		}
		return store.UpdateSelfImprovementFeedbackStatusRow(tx, in.FeedbackEventID, store.FeedbackStatusAnalyzed)
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return s.store.GetSelfImprovementRecommendationByFeedback(in.WorkspaceID, in.FeedbackEventID)
}

func (s *Service) UpdateRecommendationStatus(id, status string) (SelfImprovementRecommendation, error) {
	status = strings.TrimSpace(status)
	if !validRecommendationStatus(status) {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("unsupported recommendation status %q", status)}
	}
	if err := s.store.Transact(func(tx *store.Tx) error {
		return store.UpdateSelfImprovementRecommendationStatusRow(tx, id, status)
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return s.store.GetSelfImprovementRecommendation(id)
}

func (s *Service) UpsertClarification(recommendationID, author, body string) (SelfImprovementRecommendation, error) {
	recommendationID = strings.TrimSpace(recommendationID)
	body = strings.TrimSpace(body)
	if recommendationID == "" {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: "recommendation id is required"}
	}
	if body == "" {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: "clarification body is required"}
	}
	if _, err := s.store.GetSelfImprovementRecommendation(recommendationID); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	if err := s.store.Transact(func(tx *store.Tx) error {
		if err := store.UpsertSelfImprovementClarificationRow(tx, recommendationID, author, body); err != nil {
			return err
		}
		return store.UpdateSelfImprovementRecommendationStatusRow(tx, recommendationID, RecommendationStatusNeedsUserInput)
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return s.store.GetSelfImprovementRecommendation(recommendationID)
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func recommendationTarget(feedback store.SelfImprovementFeedback) (assetType, assetID, versionID string) {
	if feedback.LinkedPromptVersionID != "" {
		return "prompt", "", feedback.LinkedPromptVersionID
	}
	if len(feedback.LinkedSkillVersionIDs) > 0 {
		return "skill", "", feedback.LinkedSkillVersionIDs[0]
	}
	if len(feedback.LinkedGuardrailVersionIDs) > 0 {
		return "guardrail", "", feedback.LinkedGuardrailVersionIDs[0]
	}
	return "", "", ""
}

func validRecommendationStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RecommendationStatusRecommended, RecommendationStatusNeedsUserInput, RecommendationStatusAccepted, RecommendationStatusRejected, RecommendationStatusDeferred, RecommendationStatusDuplicate, RecommendationStatusFailed:
		return true
	default:
		return false
	}
}

func firstFeedbackLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		return line
	}
	return ""
}

func normalizeLesson(finding string) string {
	lesson := strings.ToLower(strings.TrimSpace(finding))
	lesson = strings.Trim(lesson, ".:;")
	if len(lesson) > 240 {
		lesson = strings.TrimSpace(lesson[:240])
	}
	return lesson
}
