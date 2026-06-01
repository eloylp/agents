package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
)

const (
	FeedbackStatusNew      = "new"
	FeedbackStatusIgnored  = "ignored"
	FeedbackStatusAnalyzed = "analyzed"
	FeedbackStatusFailed   = "failed"
	FeedbackTag            = "/agents improve"

	RecommendationStatusRecommended    = "recommended"
	RecommendationStatusNeedsUserInput = "needs_user_input"
	RecommendationStatusAccepted       = "accepted"
	RecommendationStatusRejected       = "rejected"
	RecommendationStatusDeferred       = "deferred"
	RecommendationStatusDuplicate      = "duplicate"
	RecommendationStatusFailed         = "failed"
)

type SelfImprovementFeedback struct {
	ID                        int64      `json:"id"`
	WorkspaceID               string     `json:"workspace"`
	RepoOwner                 string     `json:"repo_owner"`
	RepoName                  string     `json:"repo_name"`
	SourceType                string     `json:"source_type"`
	GitHubCommentID           int64      `json:"github_comment_id,omitempty"`
	GitHubReviewID            int64      `json:"github_review_id,omitempty"`
	GitHubDeliveryID          string     `json:"github_delivery_id,omitempty"`
	SourceURL                 string     `json:"source_url"`
	AuthorLogin               string     `json:"author_login"`
	AuthorAuthorized          bool       `json:"author_authorized"`
	IssueNumber               int        `json:"issue_number,omitempty"`
	PRNumber                  int        `json:"pr_number,omitempty"`
	RawBody                   string     `json:"raw_body"`
	Tag                       string     `json:"tag"`
	FilePath                  string     `json:"file_path,omitempty"`
	Line                      int        `json:"line,omitempty"`
	Side                      string     `json:"side,omitempty"`
	DiffHunk                  string     `json:"diff_hunk,omitempty"`
	CommitSHA                 string     `json:"commit_sha,omitempty"`
	GitHubCreatedAt           *time.Time `json:"github_created_at,omitempty"`
	GitHubUpdatedAt           *time.Time `json:"github_updated_at,omitempty"`
	IngestedAt                time.Time  `json:"ingested_at"`
	LinkedSpanID              string     `json:"linked_span_id,omitempty"`
	LinkedEventID             string     `json:"linked_event_id,omitempty"`
	LinkedAgentID             string     `json:"linked_agent_id,omitempty"`
	LinkedAgentName           string     `json:"linked_agent_name,omitempty"`
	LinkedPromptVersionID     string     `json:"linked_prompt_version_id,omitempty"`
	LinkedSkillVersionIDs     []string   `json:"linked_skill_version_ids,omitempty"`
	LinkedGuardrailVersionIDs []string   `json:"linked_guardrail_version_ids,omitempty"`
	LinkConfidence            string     `json:"link_confidence"`
	LinkDiagnostics           string     `json:"link_diagnostics,omitempty"`
	Status                    string     `json:"status"`
}

type SelfImprovementFeedbackInput struct {
	WorkspaceID               string
	RepoOwner                 string
	RepoName                  string
	SourceType                string
	GitHubCommentID           int64
	GitHubReviewID            int64
	GitHubDeliveryID          string
	SourceURL                 string
	AuthorLogin               string
	AuthorAuthorized          bool
	IssueNumber               int
	PRNumber                  int
	RawBody                   string
	Tag                       string
	FilePath                  string
	Line                      int
	Side                      string
	DiffHunk                  string
	CommitSHA                 string
	GitHubCreatedAt           *time.Time
	GitHubUpdatedAt           *time.Time
	LinkedSpanID              string
	LinkedEventID             string
	LinkedAgentID             string
	LinkedAgentName           string
	LinkedPromptVersionID     string
	LinkedSkillVersionIDs     []string
	LinkedGuardrailVersionIDs []string
	LinkConfidence            string
	LinkDiagnostics           string
	Status                    string
}

type SelfImprovementRecommendation struct {
	ID                      string                   `json:"id"`
	WorkspaceID             string                   `json:"workspace"`
	FeedbackEventID         int64                    `json:"feedback_event_id"`
	Type                    string                   `json:"type"`
	Status                  string                   `json:"status"`
	Confidence              string                   `json:"confidence"`
	Risk                    string                   `json:"risk"`
	Finding                 string                   `json:"finding"`
	NormalizedLesson        string                   `json:"normalized_lesson"`
	Rationale               string                   `json:"rationale"`
	EvidenceFeedbackIDs     []int64                  `json:"evidence_feedback_ids"`
	EvidenceSourceURLs      []string                 `json:"evidence_source_urls"`
	AttributionConfidence   string                   `json:"attribution_confidence"`
	TargetAssetType         string                   `json:"target_asset_type,omitempty"`
	TargetAssetID           string                   `json:"target_asset_id,omitempty"`
	TargetBaseVersionID     string                   `json:"target_base_version_id,omitempty"`
	ProposedPatch           string                   `json:"proposed_patch,omitempty"`
	ProposedNewBody         string                   `json:"proposed_new_body,omitempty"`
	SuggestedRolloutScope   string                   `json:"suggested_rollout_scope,omitempty"`
	AnalyzerPromptRef       string                   `json:"analyzer_prompt_ref"`
	AnalyzerPromptVersionID string                   `json:"analyzer_prompt_version_id,omitempty"`
	StructuredOutput        map[string]any           `json:"structured_output,omitempty"`
	Error                   string                   `json:"error,omitempty"`
	CreatedAt               string                   `json:"created_at"`
	UpdatedAt               string                   `json:"updated_at"`
	Feedback                *SelfImprovementFeedback `json:"feedback,omitempty"`
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

func (s *Store) UpsertSelfImprovementFeedback(in SelfImprovementFeedbackInput) (SelfImprovementFeedback, error) {
	return UpsertSelfImprovementFeedback(s.db, in)
}

func (s *Store) IgnoreSelfImprovementFeedback(in SelfImprovementFeedbackInput) (bool, error) {
	return IgnoreSelfImprovementFeedback(s.db, in)
}

func (s *Store) ListSelfImprovementFeedback(workspace, status string, limit int) ([]SelfImprovementFeedback, error) {
	return ListSelfImprovementFeedback(s.db, workspace, status, limit)
}

func (s *Store) GetSelfImprovementFeedback(id int64) (SelfImprovementFeedback, error) {
	return GetSelfImprovementFeedback(s.db, id)
}

func (s *Store) UpsertSelfImprovementRecommendation(in SelfImprovementRecommendationInput) (SelfImprovementRecommendation, error) {
	return UpsertSelfImprovementRecommendation(s.db, in)
}

func (s *Store) ListSelfImprovementRecommendations(workspace, status string, limit int) ([]SelfImprovementRecommendation, error) {
	return ListSelfImprovementRecommendations(s.db, workspace, status, limit)
}

func (s *Store) GetSelfImprovementRecommendation(id string) (SelfImprovementRecommendation, error) {
	return GetSelfImprovementRecommendation(s.db, id)
}

func (s *Store) UpdateSelfImprovementRecommendationStatus(id, status string) (SelfImprovementRecommendation, error) {
	return UpdateSelfImprovementRecommendationStatus(s.db, id, status)
}

func (s *Store) MarkSelfImprovementFeedbackFailed(id int64, cause string) error {
	return MarkSelfImprovementFeedbackFailed(s.db, id, cause)
}

func UpsertSelfImprovementFeedback(db *sql.DB, in SelfImprovementFeedbackInput) (SelfImprovementFeedback, error) {
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = FeedbackTag
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = FeedbackStatusNew
	}
	confidence := strings.TrimSpace(in.LinkConfidence)
	if confidence == "" {
		confidence = "unresolved"
	}
	_, err := db.Exec(
		`INSERT INTO self_improvement_feedback (
			workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, linked_span_id, linked_event_id, linked_agent_id, linked_agent_name,
			linked_prompt_version_id, linked_skill_version_ids, linked_guardrail_version_ids,
			link_confidence, link_diagnostics, status
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, source_type, github_comment_id, github_review_id, tag) DO UPDATE SET
			github_delivery_id=excluded.github_delivery_id,
			source_url=excluded.source_url,
			author_login=excluded.author_login,
			author_authorized=excluded.author_authorized,
			issue_number=excluded.issue_number,
			pr_number=excluded.pr_number,
			raw_body=excluded.raw_body,
			file_path=excluded.file_path,
			line=excluded.line,
			side=excluded.side,
			diff_hunk=excluded.diff_hunk,
			commit_sha=excluded.commit_sha,
			github_updated_at=excluded.github_updated_at,
			linked_span_id=excluded.linked_span_id,
			linked_event_id=excluded.linked_event_id,
			linked_agent_id=excluded.linked_agent_id,
			linked_agent_name=excluded.linked_agent_name,
			linked_prompt_version_id=excluded.linked_prompt_version_id,
			linked_skill_version_ids=excluded.linked_skill_version_ids,
			linked_guardrail_version_ids=excluded.linked_guardrail_version_ids,
			link_confidence=excluded.link_confidence,
			link_diagnostics=excluded.link_diagnostics,
			status=CASE
				WHEN excluded.status = 'ignored' THEN excluded.status
				WHEN self_improvement_feedback.raw_body <> excluded.raw_body THEN excluded.status
				ELSE self_improvement_feedback.status
			END`,
		workspaceID, strings.TrimSpace(in.RepoOwner), strings.TrimSpace(in.RepoName), strings.TrimSpace(in.SourceType),
		in.GitHubCommentID, in.GitHubReviewID, strings.TrimSpace(in.GitHubDeliveryID), strings.TrimSpace(in.SourceURL),
		strings.TrimSpace(in.AuthorLogin), boolInt(in.AuthorAuthorized), in.IssueNumber, in.PRNumber, in.RawBody, tag,
		strings.TrimSpace(in.FilePath), in.Line, strings.TrimSpace(in.Side), in.DiffHunk, strings.TrimSpace(in.CommitSHA),
		in.GitHubCreatedAt, in.GitHubUpdatedAt, strings.TrimSpace(in.LinkedSpanID), strings.TrimSpace(in.LinkedEventID),
		strings.TrimSpace(in.LinkedAgentID), strings.TrimSpace(in.LinkedAgentName), strings.TrimSpace(in.LinkedPromptVersionID),
		strings.Join(in.LinkedSkillVersionIDs, ","), strings.Join(in.LinkedGuardrailVersionIDs, ","), confidence,
		strings.TrimSpace(in.LinkDiagnostics), status,
	)
	if err != nil {
		return SelfImprovementFeedback{}, err
	}
	row := db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
			linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
			linked_guardrail_version_ids, link_confidence, link_diagnostics, status
		FROM self_improvement_feedback
		WHERE workspace_id=? AND source_type=? AND github_comment_id=? AND github_review_id=? AND tag=?`,
		workspaceID, strings.TrimSpace(in.SourceType), in.GitHubCommentID, in.GitHubReviewID, tag,
	)
	return scanSelfImprovementFeedback(row)
}

func RecommendationFromFeedback(feedback SelfImprovementFeedback) SelfImprovementRecommendationInput {
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

func recommendationTarget(feedback SelfImprovementFeedback) (assetType, assetID, versionID string) {
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

func IgnoreSelfImprovementFeedback(db *sql.DB, in SelfImprovementFeedbackInput) (bool, error) {
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = FeedbackTag
	}
	res, err := db.Exec(
		`UPDATE self_improvement_feedback SET
			github_delivery_id=?,
			source_url=?,
			author_login=?,
			author_authorized=?,
			issue_number=?,
			pr_number=?,
			raw_body=?,
			file_path=?,
			line=?,
			side=?,
			diff_hunk=?,
			commit_sha=?,
			github_updated_at=?,
			status=?
		WHERE workspace_id=? AND source_type=? AND github_comment_id=? AND github_review_id=? AND tag=?`,
		strings.TrimSpace(in.GitHubDeliveryID), strings.TrimSpace(in.SourceURL), strings.TrimSpace(in.AuthorLogin),
		boolInt(in.AuthorAuthorized), in.IssueNumber, in.PRNumber, in.RawBody, strings.TrimSpace(in.FilePath),
		in.Line, strings.TrimSpace(in.Side), in.DiffHunk, strings.TrimSpace(in.CommitSHA), in.GitHubUpdatedAt,
		FeedbackStatusIgnored, workspaceID, strings.TrimSpace(in.SourceType), in.GitHubCommentID, in.GitHubReviewID, tag,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func ListSelfImprovementFeedback(db *sql.DB, workspace, status string, limit int) ([]SelfImprovementFeedback, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	workspaceID := fleet.NormalizeWorkspaceID(workspace)
	status = strings.TrimSpace(status)
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
				raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
				github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
				linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
				linked_guardrail_version_ids, link_confidence, link_diagnostics, status
			FROM self_improvement_feedback
			WHERE workspace_id=?
			ORDER BY ingested_at DESC, id DESC LIMIT ?`,
			workspaceID, limit,
		)
	} else {
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
				raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
				github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
				linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
				linked_guardrail_version_ids, link_confidence, link_diagnostics, status
			FROM self_improvement_feedback
			WHERE workspace_id=? AND status=?
			ORDER BY ingested_at DESC, id DESC LIMIT ?`,
			workspaceID, status, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SelfImprovementFeedback
	for rows.Next() {
		ev, err := scanSelfImprovementFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func GetSelfImprovementFeedback(db *sql.DB, id int64) (SelfImprovementFeedback, error) {
	row := db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
			linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
			linked_guardrail_version_ids, link_confidence, link_diagnostics, status
		FROM self_improvement_feedback
		WHERE id=?`,
		id,
	)
	ev, err := scanSelfImprovementFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementFeedback{}, &ErrNotFound{Msg: fmt.Sprintf("feedback %d not found", id)}
	}
	return ev, err
}

func UpsertSelfImprovementRecommendation(db *sql.DB, in SelfImprovementRecommendationInput) (SelfImprovementRecommendation, error) {
	if in.FeedbackEventID <= 0 {
		return SelfImprovementRecommendation{}, &ErrValidation{Msg: "feedback_event_id is required"}
	}
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = "needs_more_context"
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = RecommendationStatusRecommended
	}
	if !validRecommendationStatus(status) {
		return SelfImprovementRecommendation{}, &ErrValidation{Msg: fmt.Sprintf("unsupported recommendation status %q", status)}
	}
	confidence := strings.TrimSpace(in.Confidence)
	if confidence == "" {
		confidence = "low"
	}
	risk := strings.TrimSpace(in.Risk)
	if risk == "" {
		risk = "low"
	}
	attribution := strings.TrimSpace(in.AttributionConfidence)
	if attribution == "" {
		attribution = "unresolved"
	}
	promptRef := strings.TrimSpace(in.AnalyzerPromptRef)
	if promptRef == "" {
		promptRef = "prompt_self-improvement-analyst"
	}
	structured, err := json.Marshal(in.StructuredOutput)
	if err != nil {
		return SelfImprovementRecommendation{}, &ErrValidation{Msg: fmt.Sprintf("structured output: %v", err)}
	}
	if string(structured) == "null" {
		structured = []byte("{}")
	}
	_, err = db.Exec(
		`INSERT INTO self_improvement_recommendations (
			id, workspace_id, feedback_event_id, type, status, confidence, risk, finding,
			normalized_lesson, rationale, evidence_feedback_ids, evidence_source_urls,
			attribution_confidence, target_asset_type, target_asset_id, target_base_version_id,
			proposed_patch, proposed_new_body, suggested_rollout_scope, analyzer_prompt_ref,
			analyzer_prompt_version_id, structured_output, error
		) VALUES ('rec_' || lower(hex(randomblob(16))),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, feedback_event_id) DO UPDATE SET
			type=excluded.type,
			status=excluded.status,
			confidence=excluded.confidence,
			risk=excluded.risk,
			finding=excluded.finding,
			normalized_lesson=excluded.normalized_lesson,
			rationale=excluded.rationale,
			evidence_feedback_ids=excluded.evidence_feedback_ids,
			evidence_source_urls=excluded.evidence_source_urls,
			attribution_confidence=excluded.attribution_confidence,
			target_asset_type=excluded.target_asset_type,
			target_asset_id=excluded.target_asset_id,
			target_base_version_id=excluded.target_base_version_id,
			proposed_patch=excluded.proposed_patch,
			proposed_new_body=excluded.proposed_new_body,
			suggested_rollout_scope=excluded.suggested_rollout_scope,
			analyzer_prompt_ref=excluded.analyzer_prompt_ref,
			analyzer_prompt_version_id=excluded.analyzer_prompt_version_id,
			structured_output=excluded.structured_output,
			error=excluded.error,
			updated_at=datetime('now')`,
		workspaceID, in.FeedbackEventID, typ, status, confidence, risk, strings.TrimSpace(in.Finding),
		strings.TrimSpace(in.NormalizedLesson), strings.TrimSpace(in.Rationale), joinInt64s(in.EvidenceFeedbackIDs),
		strings.Join(trimStrings(in.EvidenceSourceURLs), ","), attribution, strings.TrimSpace(in.TargetAssetType),
		strings.TrimSpace(in.TargetAssetID), strings.TrimSpace(in.TargetBaseVersionID), strings.TrimSpace(in.ProposedPatch),
		strings.TrimSpace(in.ProposedNewBody), strings.TrimSpace(in.SuggestedRolloutScope), promptRef,
		strings.TrimSpace(in.AnalyzerPromptVersionID), string(structured), strings.TrimSpace(in.Error),
	)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	if _, err := db.Exec(`UPDATE self_improvement_feedback SET status=? WHERE id=?`, FeedbackStatusAnalyzed, in.FeedbackEventID); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	return getSelfImprovementRecommendationByFeedback(db, workspaceID, in.FeedbackEventID)
}

func ListSelfImprovementRecommendations(db *sql.DB, workspace, status string, limit int) ([]SelfImprovementRecommendation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	workspaceID := fleet.NormalizeWorkspaceID(workspace)
	status = strings.TrimSpace(status)
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = db.Query(recommendationSelectSQL()+` WHERE r.workspace_id=? ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, workspaceID, limit)
	} else {
		rows, err = db.Query(recommendationSelectSQL()+` WHERE r.workspace_id=? AND r.status=? ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, workspaceID, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SelfImprovementRecommendation
	for rows.Next() {
		rec, err := scanSelfImprovementRecommendation(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func GetSelfImprovementRecommendation(db *sql.DB, id string) (SelfImprovementRecommendation, error) {
	row := db.QueryRow(recommendationSelectSQL()+` WHERE r.id=?`, strings.TrimSpace(id))
	rec, err := scanSelfImprovementRecommendation(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementRecommendation{}, &ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	return rec, err
}

func UpdateSelfImprovementRecommendationStatus(db *sql.DB, id, status string) (SelfImprovementRecommendation, error) {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	if id == "" {
		return SelfImprovementRecommendation{}, &ErrValidation{Msg: "recommendation id is required"}
	}
	if !validRecommendationStatus(status) {
		return SelfImprovementRecommendation{}, &ErrValidation{Msg: fmt.Sprintf("unsupported recommendation status %q", status)}
	}
	res, err := db.Exec(`UPDATE self_improvement_recommendations SET status=?, updated_at=datetime('now') WHERE id=?`, status, id)
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return SelfImprovementRecommendation{}, err
	}
	if affected == 0 {
		return SelfImprovementRecommendation{}, &ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	return GetSelfImprovementRecommendation(db, id)
}

func MarkSelfImprovementFeedbackFailed(db *sql.DB, id int64, cause string) error {
	if id <= 0 {
		return &ErrValidation{Msg: "feedback id is required"}
	}
	res, err := db.Exec(`UPDATE self_improvement_feedback SET status=? WHERE id=?`, FeedbackStatusFailed, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("feedback %d not found", id)}
	}
	_, err = db.Exec(
		`UPDATE self_improvement_recommendations
		SET status=?, error=?, updated_at=datetime('now')
		WHERE feedback_event_id=?`,
		RecommendationStatusFailed, strings.TrimSpace(cause), id,
	)
	return err
}

func getSelfImprovementRecommendationByFeedback(db *sql.DB, workspaceID string, feedbackID int64) (SelfImprovementRecommendation, error) {
	row := db.QueryRow(recommendationSelectSQL()+` WHERE r.workspace_id=? AND r.feedback_event_id=?`, workspaceID, feedbackID)
	return scanSelfImprovementRecommendation(row, true)
}

type selfImprovementScanner interface {
	Scan(dest ...any) error
}

func scanSelfImprovementFeedback(row selfImprovementScanner) (SelfImprovementFeedback, error) {
	var ev SelfImprovementFeedback
	var authorized int
	var skillIDs, guardrailIDs string
	if err := row.Scan(
		&ev.ID, &ev.WorkspaceID, &ev.RepoOwner, &ev.RepoName, &ev.SourceType, &ev.GitHubCommentID, &ev.GitHubReviewID,
		&ev.GitHubDeliveryID, &ev.SourceURL, &ev.AuthorLogin, &authorized, &ev.IssueNumber, &ev.PRNumber,
		&ev.RawBody, &ev.Tag, &ev.FilePath, &ev.Line, &ev.Side, &ev.DiffHunk, &ev.CommitSHA, &ev.GitHubCreatedAt,
		&ev.GitHubUpdatedAt, &ev.IngestedAt, &ev.LinkedSpanID, &ev.LinkedEventID, &ev.LinkedAgentID,
		&ev.LinkedAgentName, &ev.LinkedPromptVersionID, &skillIDs, &guardrailIDs, &ev.LinkConfidence,
		&ev.LinkDiagnostics, &ev.Status,
	); err != nil {
		return SelfImprovementFeedback{}, err
	}
	ev.AuthorAuthorized = authorized == 1
	ev.LinkedSkillVersionIDs = splitCSV(skillIDs)
	ev.LinkedGuardrailVersionIDs = splitCSV(guardrailIDs)
	return ev, nil
}

func recommendationSelectSQL() string {
	return `SELECT r.id, r.workspace_id, r.feedback_event_id, r.type, r.status, r.confidence, r.risk,
		r.finding, r.normalized_lesson, r.rationale, r.evidence_feedback_ids, r.evidence_source_urls,
		r.attribution_confidence, r.target_asset_type, r.target_asset_id, r.target_base_version_id,
		r.proposed_patch, r.proposed_new_body, r.suggested_rollout_scope, r.analyzer_prompt_ref,
		r.analyzer_prompt_version_id, r.structured_output, r.error, r.created_at, r.updated_at,
		f.id, f.workspace_id, f.repo_owner, f.repo_name, f.source_type, f.github_comment_id, f.github_review_id,
		f.github_delivery_id, f.source_url, f.author_login, f.author_authorized, f.issue_number, f.pr_number,
		f.raw_body, f.tag, f.file_path, f.line, f.side, f.diff_hunk, f.commit_sha, f.github_created_at,
		f.github_updated_at, f.ingested_at, f.linked_span_id, f.linked_event_id, f.linked_agent_id,
		f.linked_agent_name, f.linked_prompt_version_id, f.linked_skill_version_ids,
		f.linked_guardrail_version_ids, f.link_confidence, f.link_diagnostics, f.status
	FROM self_improvement_recommendations r
	JOIN self_improvement_feedback f ON f.id = r.feedback_event_id`
}

func scanSelfImprovementRecommendation(row selfImprovementScanner, includeFeedback bool) (SelfImprovementRecommendation, error) {
	var rec SelfImprovementRecommendation
	var feedback SelfImprovementFeedback
	var evidenceIDs, evidenceURLs, structured string
	var authorized int
	var skillIDs, guardrailIDs string
	if err := row.Scan(
		&rec.ID, &rec.WorkspaceID, &rec.FeedbackEventID, &rec.Type, &rec.Status, &rec.Confidence, &rec.Risk,
		&rec.Finding, &rec.NormalizedLesson, &rec.Rationale, &evidenceIDs, &evidenceURLs,
		&rec.AttributionConfidence, &rec.TargetAssetType, &rec.TargetAssetID, &rec.TargetBaseVersionID,
		&rec.ProposedPatch, &rec.ProposedNewBody, &rec.SuggestedRolloutScope, &rec.AnalyzerPromptRef,
		&rec.AnalyzerPromptVersionID, &structured, &rec.Error, &rec.CreatedAt, &rec.UpdatedAt,
		&feedback.ID, &feedback.WorkspaceID, &feedback.RepoOwner, &feedback.RepoName, &feedback.SourceType,
		&feedback.GitHubCommentID, &feedback.GitHubReviewID, &feedback.GitHubDeliveryID, &feedback.SourceURL,
		&feedback.AuthorLogin, &authorized, &feedback.IssueNumber, &feedback.PRNumber, &feedback.RawBody,
		&feedback.Tag, &feedback.FilePath, &feedback.Line, &feedback.Side, &feedback.DiffHunk,
		&feedback.CommitSHA, &feedback.GitHubCreatedAt, &feedback.GitHubUpdatedAt, &feedback.IngestedAt,
		&feedback.LinkedSpanID, &feedback.LinkedEventID, &feedback.LinkedAgentID, &feedback.LinkedAgentName,
		&feedback.LinkedPromptVersionID, &skillIDs, &guardrailIDs, &feedback.LinkConfidence,
		&feedback.LinkDiagnostics, &feedback.Status,
	); err != nil {
		return SelfImprovementRecommendation{}, err
	}
	rec.EvidenceFeedbackIDs = splitInt64CSV(evidenceIDs)
	rec.EvidenceSourceURLs = splitCSV(evidenceURLs)
	if structured != "" {
		_ = json.Unmarshal([]byte(structured), &rec.StructuredOutput)
	}
	feedback.AuthorAuthorized = authorized == 1
	feedback.LinkedSkillVersionIDs = splitCSV(skillIDs)
	feedback.LinkedGuardrailVersionIDs = splitCSV(guardrailIDs)
	if includeFeedback {
		rec.Feedback = &feedback
	}
	return rec, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func trimStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func joinInt64s(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value > 0 {
			parts = append(parts, fmt.Sprint(value))
		}
	}
	return strings.Join(parts, ",")
}

func splitInt64CSV(s string) []int64 {
	parts := splitCSV(s)
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		var value int64
		if _, err := fmt.Sscan(part, &value); err == nil && value > 0 {
			out = append(out, value)
		}
	}
	return out
}

func validRecommendationStatus(status string) bool {
	return slices.Contains([]string{
		RecommendationStatusRecommended,
		RecommendationStatusNeedsUserInput,
		RecommendationStatusAccepted,
		RecommendationStatusRejected,
		RecommendationStatusDeferred,
		RecommendationStatusDuplicate,
		RecommendationStatusFailed,
	}, status)
}

func firstFeedbackLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, FeedbackTag, ""))
		if line != "" {
			if len(line) > 500 {
				return line[:500]
			}
			return line
		}
	}
	return ""
}

func normalizeLesson(finding string) string {
	finding = strings.TrimSpace(strings.ReplaceAll(finding, FeedbackTag, ""))
	finding = strings.TrimSuffix(finding, ".")
	if finding == "" {
		return "Review self-improvement feedback before changing catalog guidance"
	}
	return finding
}
