package selfimprovement

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/store"
)

const (
	RecommendationStatusRecommended    = "recommended"
	RecommendationStatusNeedsUserInput = "needs_user_input"
	RecommendationStatusAnalyzing      = "analyzing"
	RecommendationStatusClarifying     = "clarifying"
	RecommendationStatusRejected       = "rejected"
	RecommendationStatusFailed         = "failed"
)

type SelfImprovementRecommendation struct {
	ID                      string                         `json:"id"`
	WorkspaceID             string                         `json:"workspace"`
	FeedbackEventID         int64                          `json:"feedback_event_id"`
	Type                    string                         `json:"type"`
	Status                  string                         `json:"status"`
	Confidence              string                         `json:"confidence"`
	Risk                    string                         `json:"risk"`
	Finding                 string                         `json:"finding"`
	NormalizedLesson        string                         `json:"normalized_lesson"`
	Rationale               string                         `json:"rationale"`
	EvidenceFeedbackIDs     []int64                        `json:"evidence_feedback_ids"`
	EvidenceSourceURLs      []string                       `json:"evidence_source_urls"`
	AttributionConfidence   string                         `json:"attribution_confidence"`
	TargetAssetType         string                         `json:"target_asset_type,omitempty"`
	TargetAssetID           string                         `json:"target_asset_id,omitempty"`
	TargetBaseVersionID     string                         `json:"target_base_version_id,omitempty"`
	ProposedPatch           string                         `json:"proposed_patch,omitempty"`
	ProposedNewBody         string                         `json:"proposed_new_body,omitempty"`
	SuggestedRolloutScope   string                         `json:"suggested_rollout_scope,omitempty"`
	AnalyzerPromptRef       string                         `json:"analyzer_prompt_ref"`
	AnalyzerPromptVersionID string                         `json:"analyzer_prompt_version_id,omitempty"`
	StructuredOutput        map[string]any                 `json:"structured_output,omitempty"`
	Error                   string                         `json:"error,omitempty"`
	DecisionReason          string                         `json:"decision_reason,omitempty"`
	CreatedAt               string                         `json:"created_at"`
	UpdatedAt               string                         `json:"updated_at"`
	Feedback                *store.SelfImprovementFeedback `json:"feedback,omitempty"`
	Clarification           *SelfImprovementClarification  `json:"clarification,omitempty"`
	ProposalBundle          *SelfImprovementProposalBundle `json:"proposal_bundle,omitempty"`
}

type SelfImprovementClarification struct {
	RecommendationID string `json:"recommendation_id"`
	Author           string `json:"author"`
	Body             string `json:"body"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type SelfImprovementRecommendationInput struct {
	WorkspaceID             string
	FeedbackEventID         int64
	Type                    string
	Status                  string
	Confidence              string
	Risk                    string
	Finding                 string
	NormalizedLesson        string
	Rationale               string
	EvidenceFeedbackIDs     []int64
	EvidenceSourceURLs      []string
	AttributionConfidence   string
	TargetAssetType         string
	TargetAssetID           string
	TargetBaseVersionID     string
	ProposedPatch           string
	ProposedNewBody         string
	SuggestedRolloutScope   string
	AnalyzerPromptRef       string
	AnalyzerPromptVersionID string
	StructuredOutput        map[string]any
	Error                   string
}

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
	if existing, err := s.store.GetSelfImprovementRecommendationByFeedback(in.WorkspaceID, in.FeedbackEventID); err == nil {
		if terminalRecommendationStatus(existing.Status) {
			if err := s.store.Transact(func(tx *store.Tx) error {
				return store.UpdateSelfImprovementFeedbackStatusRow(tx, in.FeedbackEventID, store.FeedbackStatusAnalyzed)
			}); err != nil {
				return SelfImprovementRecommendation{}, err
			}
			return recommendationFromRow(existing), nil
		}
	} else {
		var nf *store.ErrNotFound
		if !errors.As(err, &nf) {
			return SelfImprovementRecommendation{}, err
		}
	}
	if in.StructuredOutput == nil {
		in.StructuredOutput = map[string]any{}
	}
	if err := s.store.Transact(func(tx *store.Tx) error {
		if err := store.UpsertSelfImprovementRecommendationRow(tx, recommendationInputRow(in)); err != nil {
			return err
		}
		return store.UpdateSelfImprovementFeedbackStatusRow(tx, in.FeedbackEventID, store.FeedbackStatusAnalyzed)
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	row, err := s.store.GetSelfImprovementRecommendationByFeedback(in.WorkspaceID, in.FeedbackEventID)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	rec := recommendationFromRow(row)
	if rec.Status == RecommendationStatusRecommended {
		if _, err := createSelfImprovementProposalBundle(s.store, rec.ID); err != nil {
			var validation *store.ErrValidation
			var conflict *store.ErrConflict
			var notFound *store.ErrNotFound
			if errors.As(err, &validation) || errors.As(err, &conflict) || errors.As(err, &notFound) {
				if markErr := s.store.Transact(func(tx *store.Tx) error {
					return store.UpdateSelfImprovementRecommendationStatusErrorRow(tx, rec.ID, RecommendationStatusNeedsUserInput, "Could not create editable proposal bundle: "+err.Error())
				}); markErr != nil {
					return SelfImprovementRecommendation{}, markErr
				}
				return s.GetRecommendation(rec.ID)
			}
			return SelfImprovementRecommendation{}, err
		}
		return s.GetRecommendation(rec.ID)
	}
	return rec, nil
}

func (s *Service) UpdateRecommendationStatus(id, status, reason string) (SelfImprovementRecommendation, error) {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if !validHumanRecommendationStatus(status) {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("unsupported recommendation decision %q", status)}
	}
	current, err := s.GetRecommendation(id)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	if terminalRecommendationStatus(current.Status) {
		if current.Status == status {
			return current, nil
		}
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation %q is already %s and cannot be changed", id, current.Status)}
	}
	if current.Status == RecommendationStatusAnalyzing || current.Status == RecommendationStatusClarifying {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation %q is currently %s and cannot be changed", id, current.Status)}
	}
	if err := s.store.Transact(func(tx *store.Tx) error {
		if err := store.UpdateSelfImprovementRecommendationDecisionRow(tx, id, status, reason); err != nil {
			return err
		}
		if status != RecommendationStatusRejected {
			return nil
		}
		bundle, err := getSelfImprovementProposalBundle(tx, id)
		if err != nil {
			var nf *store.ErrNotFound
			if errors.As(err, &nf) {
				return nil
			}
			return err
		}
		if bundle.Status != ProposalBundleStatusPending {
			return nil
		}
		if err := store.UpdateSelfImprovementProposalBundleStatusRow(tx, bundle.ID, ProposalBundleStatusDiscarded); err != nil {
			return err
		}
		if err := store.DiscardPendingSelfImprovementProposalBundleItemRows(tx, bundle.ID, ProposalBundleDecisionDiscarded); err != nil {
			return err
		}
		for _, item := range bundle.Items {
			before := bundleItemAuditSnapshot(item)
			after := item
			if item.Decision == ProposalBundleDecisionAccepted || item.Decision == ProposalBundleDecisionPending {
				after.Decision = ProposalBundleDecisionDiscarded
			}
			if err := insertBundleItemEvent(tx, bundle.ID, item.ID, "discarded", "dashboard", reason, before, bundleItemAuditSnapshot(after)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return s.GetRecommendation(id)
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
	rec, err := s.GetRecommendation(recommendationID)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	if terminalRecommendationStatus(rec.Status) {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation %q is already %s and cannot be clarified", recommendationID, rec.Status)}
	}
	if rec.Status != RecommendationStatusNeedsUserInput && rec.Status != RecommendationStatusFailed {
		return SelfImprovementRecommendation{}, &store.ErrValidation{Msg: fmt.Sprintf("recommendation %q is %s and cannot be clarified", recommendationID, rec.Status)}
	}
	if err := s.store.Transact(func(tx *store.Tx) error {
		if err := store.UpsertSelfImprovementClarificationRow(tx, recommendationID, author, body); err != nil {
			return err
		}
		return store.UpdateSelfImprovementRecommendationStatusRow(tx, recommendationID, RecommendationStatusClarifying)
	}); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return s.GetRecommendation(recommendationID)
}

func recommendationInputRow(in SelfImprovementRecommendationInput) store.SelfImprovementRecommendationInputRow {
	return store.SelfImprovementRecommendationInputRow{
		WorkspaceID:             in.WorkspaceID,
		FeedbackEventID:         in.FeedbackEventID,
		Type:                    in.Type,
		Status:                  in.Status,
		Confidence:              in.Confidence,
		Risk:                    in.Risk,
		Finding:                 in.Finding,
		NormalizedLesson:        in.NormalizedLesson,
		Rationale:               in.Rationale,
		EvidenceFeedbackIDs:     in.EvidenceFeedbackIDs,
		EvidenceSourceURLs:      in.EvidenceSourceURLs,
		AttributionConfidence:   in.AttributionConfidence,
		TargetAssetType:         in.TargetAssetType,
		TargetAssetID:           in.TargetAssetID,
		TargetBaseVersionID:     in.TargetBaseVersionID,
		ProposedPatch:           in.ProposedPatch,
		ProposedNewBody:         in.ProposedNewBody,
		SuggestedRolloutScope:   in.SuggestedRolloutScope,
		AnalyzerPromptRef:       in.AnalyzerPromptRef,
		AnalyzerPromptVersionID: in.AnalyzerPromptVersionID,
		StructuredOutput:        in.StructuredOutput,
		Error:                   in.Error,
	}
}

func recommendationFromRow(row store.SelfImprovementRecommendationRow) SelfImprovementRecommendation {
	var clarification *SelfImprovementClarification
	if row.Clarification != nil {
		clarification = &SelfImprovementClarification{
			RecommendationID: row.Clarification.RecommendationID,
			Author:           row.Clarification.Author,
			Body:             row.Clarification.Body,
			CreatedAt:        row.Clarification.CreatedAt,
			UpdatedAt:        row.Clarification.UpdatedAt,
		}
	}
	var bundle *SelfImprovementProposalBundle
	if row.ProposalBundle != nil {
		converted := proposalBundleFromRow(*row.ProposalBundle)
		bundle = &converted
	}
	rec := SelfImprovementRecommendation{
		ID:                      row.ID,
		WorkspaceID:             row.WorkspaceID,
		FeedbackEventID:         row.FeedbackEventID,
		Type:                    row.Type,
		Status:                  row.Status,
		Confidence:              row.Confidence,
		Risk:                    row.Risk,
		Finding:                 row.Finding,
		NormalizedLesson:        row.NormalizedLesson,
		Rationale:               row.Rationale,
		EvidenceFeedbackIDs:     row.EvidenceFeedbackIDs,
		EvidenceSourceURLs:      row.EvidenceSourceURLs,
		AttributionConfidence:   row.AttributionConfidence,
		TargetAssetType:         row.TargetAssetType,
		TargetAssetID:           row.TargetAssetID,
		TargetBaseVersionID:     row.TargetBaseVersionID,
		ProposedPatch:           row.ProposedPatch,
		ProposedNewBody:         row.ProposedNewBody,
		SuggestedRolloutScope:   row.SuggestedRolloutScope,
		AnalyzerPromptRef:       row.AnalyzerPromptRef,
		AnalyzerPromptVersionID: row.AnalyzerPromptVersionID,
		StructuredOutput:        row.StructuredOutput,
		Error:                   row.Error,
		DecisionReason:          row.DecisionReason,
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
		Feedback:                row.Feedback,
		Clarification:           clarification,
		ProposalBundle:          bundle,
	}
	return rec
}

func recommendationRowFromRecommendation(rec SelfImprovementRecommendation) store.SelfImprovementRecommendationRow {
	var clarification *store.SelfImprovementClarificationRow
	if rec.Clarification != nil {
		clarification = &store.SelfImprovementClarificationRow{
			RecommendationID: rec.Clarification.RecommendationID,
			Author:           rec.Clarification.Author,
			Body:             rec.Clarification.Body,
			CreatedAt:        rec.Clarification.CreatedAt,
			UpdatedAt:        rec.Clarification.UpdatedAt,
		}
	}
	var bundle *store.SelfImprovementProposalBundleRow
	if rec.ProposalBundle != nil {
		converted := proposalBundleRowFromBundle(*rec.ProposalBundle)
		bundle = &converted
	}
	return store.SelfImprovementRecommendationRow{
		ID:                      rec.ID,
		WorkspaceID:             rec.WorkspaceID,
		FeedbackEventID:         rec.FeedbackEventID,
		Type:                    rec.Type,
		Status:                  rec.Status,
		Confidence:              rec.Confidence,
		Risk:                    rec.Risk,
		Finding:                 rec.Finding,
		NormalizedLesson:        rec.NormalizedLesson,
		Rationale:               rec.Rationale,
		EvidenceFeedbackIDs:     rec.EvidenceFeedbackIDs,
		EvidenceSourceURLs:      rec.EvidenceSourceURLs,
		AttributionConfidence:   rec.AttributionConfidence,
		TargetAssetType:         rec.TargetAssetType,
		TargetAssetID:           rec.TargetAssetID,
		TargetBaseVersionID:     rec.TargetBaseVersionID,
		ProposedPatch:           rec.ProposedPatch,
		ProposedNewBody:         rec.ProposedNewBody,
		SuggestedRolloutScope:   rec.SuggestedRolloutScope,
		AnalyzerPromptRef:       rec.AnalyzerPromptRef,
		AnalyzerPromptVersionID: rec.AnalyzerPromptVersionID,
		StructuredOutput:        rec.StructuredOutput,
		Error:                   rec.Error,
		CreatedAt:               rec.CreatedAt,
		UpdatedAt:               rec.UpdatedAt,
		Feedback:                rec.Feedback,
		Clarification:           clarification,
		ProposalBundle:          bundle,
	}
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
	case RecommendationStatusRecommended, RecommendationStatusNeedsUserInput, RecommendationStatusAnalyzing, RecommendationStatusClarifying, RecommendationStatusRejected, RecommendationStatusFailed:
		return true
	default:
		return false
	}
}

func validHumanRecommendationStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RecommendationStatusRejected:
		return true
	default:
		return false
	}
}

func terminalRecommendationStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RecommendationStatusRejected:
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
