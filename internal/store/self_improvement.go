package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

	SelfImprovementAllWorkspaces = "*"
)

type SelfImprovementFeedback struct {
	ID                        int64      `json:"id"`
	WorkspaceID               string     `json:"workspace"`
	RepoOwner                 string     `json:"repo_owner"`
	RepoName                  string     `json:"repo_name"`
	SourceType                string     `json:"source_type"`
	GitHubCommentID           int64      `json:"github_comment_id,omitempty"`
	GitHubReviewID            int64      `json:"github_review_id,omitempty"`
	GitHubReviewCommentID     int64      `json:"github_review_comment_id,omitempty"`
	GitHubParentCommentID     int64      `json:"github_parent_comment_id,omitempty"`
	GitHubPullRequestReviewID int64      `json:"github_pull_request_review_id,omitempty"`
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
	GitHubReviewCommentID     int64
	GitHubParentCommentID     int64
	GitHubPullRequestReviewID int64
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

type SelfImprovementRecommendationRow struct {
	ID                      string                            `json:"id"`
	WorkspaceID             string                            `json:"workspace"`
	FeedbackEventID         int64                             `json:"feedback_event_id"`
	Type                    string                            `json:"type"`
	Status                  string                            `json:"status"`
	Confidence              string                            `json:"confidence"`
	Risk                    string                            `json:"risk"`
	Finding                 string                            `json:"finding"`
	NormalizedLesson        string                            `json:"normalized_lesson"`
	Rationale               string                            `json:"rationale"`
	EvidenceFeedbackIDs     []int64                           `json:"evidence_feedback_ids"`
	EvidenceSourceURLs      []string                          `json:"evidence_source_urls"`
	AttributionConfidence   string                            `json:"attribution_confidence"`
	TargetAssetType         string                            `json:"target_asset_type,omitempty"`
	TargetAssetID           string                            `json:"target_asset_id,omitempty"`
	TargetBaseVersionID     string                            `json:"target_base_version_id,omitempty"`
	ProposedPatch           string                            `json:"proposed_patch,omitempty"`
	ProposedNewBody         string                            `json:"proposed_new_body,omitempty"`
	AnalyzerPromptRef       string                            `json:"analyzer_prompt_ref"`
	AnalyzerPromptVersionID string                            `json:"analyzer_prompt_version_id,omitempty"`
	StructuredOutput        map[string]any                    `json:"structured_output,omitempty"`
	Error                   string                            `json:"error,omitempty"`
	DecisionReason          string                            `json:"decision_reason,omitempty"`
	CreatedAt               string                            `json:"created_at"`
	UpdatedAt               string                            `json:"updated_at"`
	Feedback                *SelfImprovementFeedback          `json:"feedback,omitempty"`
	Clarification           *SelfImprovementClarificationRow  `json:"clarification,omitempty"`
	ProposalBundle          *SelfImprovementProposalBundleRow `json:"proposal_bundle,omitempty"`
}

type SelfImprovementClarificationRow struct {
	RecommendationID string `json:"recommendation_id"`
	Author           string `json:"author"`
	Body             string `json:"body"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type SelfImprovementRecommendationInputRow struct {
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

func (s *Store) GetSelfImprovementRecommendationByFeedback(workspaceID string, feedbackID int64) (SelfImprovementRecommendationRow, error) {
	return getSelfImprovementRecommendationByFeedback(s.db, fleet.NormalizeWorkspaceID(workspaceID), feedbackID)
}

func (s *Store) ListSelfImprovementRecommendations(workspace, status string, limit int) ([]SelfImprovementRecommendationRow, error) {
	return ListSelfImprovementRecommendations(s.db, workspace, status, limit)
}

func (s *Store) GetSelfImprovementRecommendation(id string) (SelfImprovementRecommendationRow, error) {
	return GetSelfImprovementRecommendation(s.db, id)
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
	if in.SourceType == "pull_request_review_comment" && in.GitHubReviewCommentID > 0 {
		err := upsertSelfImprovementFeedbackByReviewCommentID(db, workspaceID, tag, in, status, confidence)
		if err != nil {
			return SelfImprovementFeedback{}, err
		}
		return getSelfImprovementFeedbackByReviewCommentID(db, workspaceID, strings.TrimSpace(in.SourceType), in.GitHubReviewCommentID, tag)
	}
	_, err := db.Exec(
		`INSERT INTO self_improvement_feedback (
			workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, linked_span_id, linked_event_id, linked_agent_id, linked_agent_name,
			linked_prompt_version_id, linked_skill_version_ids, linked_guardrail_version_ids,
			link_confidence, link_diagnostics, status
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, source_type, github_comment_id, github_review_id, tag) WHERE github_review_comment_id = 0 DO UPDATE SET
			github_review_comment_id=excluded.github_review_comment_id,
			github_parent_comment_id=excluded.github_parent_comment_id,
			github_pull_request_review_id=excluded.github_pull_request_review_id,
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
		in.GitHubCommentID, in.GitHubReviewID, in.GitHubReviewCommentID, in.GitHubParentCommentID,
		in.GitHubPullRequestReviewID, strings.TrimSpace(in.GitHubDeliveryID), strings.TrimSpace(in.SourceURL),
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
			github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
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

func upsertSelfImprovementFeedbackByReviewCommentID(db *sql.DB, workspaceID, tag string, in SelfImprovementFeedbackInput, status, confidence string) error {
	_, err := db.Exec(
		`INSERT INTO self_improvement_feedback (
			workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, linked_span_id, linked_event_id, linked_agent_id, linked_agent_name,
			linked_prompt_version_id, linked_skill_version_ids, linked_guardrail_version_ids,
			link_confidence, link_diagnostics, status
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, source_type, github_review_comment_id, tag) WHERE github_review_comment_id > 0 DO UPDATE SET
			repo_owner=excluded.repo_owner,
			repo_name=excluded.repo_name,
			github_comment_id=0,
			github_review_id=0,
			github_parent_comment_id=excluded.github_parent_comment_id,
			github_pull_request_review_id=excluded.github_pull_request_review_id,
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
		0, 0, in.GitHubReviewCommentID, in.GitHubParentCommentID, in.GitHubPullRequestReviewID,
		strings.TrimSpace(in.GitHubDeliveryID), strings.TrimSpace(in.SourceURL), strings.TrimSpace(in.AuthorLogin),
		boolInt(in.AuthorAuthorized), in.IssueNumber, in.PRNumber, in.RawBody, tag, strings.TrimSpace(in.FilePath),
		in.Line, strings.TrimSpace(in.Side), in.DiffHunk, strings.TrimSpace(in.CommitSHA), in.GitHubCreatedAt,
		in.GitHubUpdatedAt, strings.TrimSpace(in.LinkedSpanID), strings.TrimSpace(in.LinkedEventID),
		strings.TrimSpace(in.LinkedAgentID), strings.TrimSpace(in.LinkedAgentName), strings.TrimSpace(in.LinkedPromptVersionID),
		strings.Join(in.LinkedSkillVersionIDs, ","), strings.Join(in.LinkedGuardrailVersionIDs, ","), confidence,
		strings.TrimSpace(in.LinkDiagnostics), status,
	)
	return err
}

func getSelfImprovementFeedbackByReviewCommentID(db *sql.DB, workspaceID, sourceType string, reviewCommentID int64, tag string) (SelfImprovementFeedback, error) {
	row := db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
			github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
			github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
			raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
			github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
			linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
			linked_guardrail_version_ids, link_confidence, link_diagnostics, status
		FROM self_improvement_feedback
		WHERE workspace_id=? AND source_type=? AND github_review_comment_id=? AND tag=?`,
		workspaceID, sourceType, reviewCommentID, tag,
	)
	return scanSelfImprovementFeedback(row)
}

func IgnoreSelfImprovementFeedback(db *sql.DB, in SelfImprovementFeedbackInput) (bool, error) {
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = FeedbackTag
	}
	if in.SourceType == "pull_request_review_comment" && in.GitHubReviewCommentID > 0 {
		res, err := db.Exec(
			`UPDATE self_improvement_feedback SET
				github_parent_comment_id=?,
				github_pull_request_review_id=?,
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
			WHERE workspace_id=? AND source_type=? AND github_review_comment_id=? AND tag=?`,
			in.GitHubParentCommentID, in.GitHubPullRequestReviewID, strings.TrimSpace(in.GitHubDeliveryID),
			strings.TrimSpace(in.SourceURL), strings.TrimSpace(in.AuthorLogin), boolInt(in.AuthorAuthorized),
			in.IssueNumber, in.PRNumber, in.RawBody, strings.TrimSpace(in.FilePath), in.Line, strings.TrimSpace(in.Side),
			in.DiffHunk, strings.TrimSpace(in.CommitSHA), in.GitHubUpdatedAt, FeedbackStatusIgnored,
			workspaceID, strings.TrimSpace(in.SourceType), in.GitHubReviewCommentID, tag,
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
	res, err := db.Exec(
		`UPDATE self_improvement_feedback SET
			github_review_comment_id=?,
			github_parent_comment_id=?,
			github_pull_request_review_id=?,
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
		in.GitHubReviewCommentID, in.GitHubParentCommentID, in.GitHubPullRequestReviewID,
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
	allWorkspaces := selfImprovementAllWorkspaces(workspace)
	workspaceID := fleet.NormalizeWorkspaceID(workspace)
	status = strings.TrimSpace(status)
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case allWorkspaces && status == "":
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
				github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
				raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
				github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
				linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
				linked_guardrail_version_ids, link_confidence, link_diagnostics, status
			FROM self_improvement_feedback
			ORDER BY ingested_at DESC, id DESC LIMIT ?`,
			limit,
		)
	case allWorkspaces:
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
				github_delivery_id, source_url, author_login, author_authorized, issue_number, pr_number,
				raw_body, tag, file_path, line, side, diff_hunk, commit_sha, github_created_at,
				github_updated_at, ingested_at, linked_span_id, linked_event_id, linked_agent_id,
				linked_agent_name, linked_prompt_version_id, linked_skill_version_ids,
				linked_guardrail_version_ids, link_confidence, link_diagnostics, status
			FROM self_improvement_feedback
			WHERE status=?
			ORDER BY ingested_at DESC, id DESC LIMIT ?`,
			status, limit,
		)
	case status == "":
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
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
	default:
		rows, err = db.Query(
			`SELECT id, workspace_id, repo_owner, repo_name, source_type, github_comment_id, github_review_id,
				github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
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
			github_review_comment_id, github_parent_comment_id, github_pull_request_review_id,
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

func UpsertSelfImprovementRecommendationRow(q sqlExec, in SelfImprovementRecommendationInputRow) error {
	if in.FeedbackEventID <= 0 {
		return &ErrValidation{Msg: "feedback_event_id is required"}
	}
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	structured, err := json.Marshal(in.StructuredOutput)
	if err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("structured output: %v", err)}
	}
	if string(structured) == "null" {
		structured = []byte("{}")
	}
	_, err = q.Exec(
		`INSERT INTO self_improvement_recommendations (
			id, workspace_id, feedback_event_id, type, status, confidence, risk, finding,
			normalized_lesson, rationale, evidence_feedback_ids, evidence_source_urls,
			attribution_confidence, target_asset_type, target_asset_id, target_base_version_id,
			proposed_patch, proposed_new_body, analyzer_prompt_ref,
			analyzer_prompt_version_id, structured_output, error
		) VALUES ('rec_' || lower(hex(randomblob(16))),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
			analyzer_prompt_ref=excluded.analyzer_prompt_ref,
			analyzer_prompt_version_id=excluded.analyzer_prompt_version_id,
			structured_output=excluded.structured_output,
			error=excluded.error,
			updated_at=datetime('now')`,
		workspaceID, in.FeedbackEventID, strings.TrimSpace(in.Type), strings.TrimSpace(in.Status),
		strings.TrimSpace(in.Confidence), strings.TrimSpace(in.Risk), strings.TrimSpace(in.Finding),
		strings.TrimSpace(in.NormalizedLesson), strings.TrimSpace(in.Rationale), joinInt64s(in.EvidenceFeedbackIDs),
		strings.Join(trimStrings(in.EvidenceSourceURLs), ","), strings.TrimSpace(in.AttributionConfidence), strings.TrimSpace(in.TargetAssetType),
		strings.TrimSpace(in.TargetAssetID), strings.TrimSpace(in.TargetBaseVersionID), strings.TrimSpace(in.ProposedPatch),
		strings.TrimSpace(in.ProposedNewBody), strings.TrimSpace(in.AnalyzerPromptRef),
		strings.TrimSpace(in.AnalyzerPromptVersionID), string(structured), strings.TrimSpace(in.Error),
	)
	return err
}

func ListSelfImprovementRecommendations(db *sql.DB, workspace, status string, limit int) ([]SelfImprovementRecommendationRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	allWorkspaces := selfImprovementAllWorkspaces(workspace)
	workspaceID := fleet.NormalizeWorkspaceID(workspace)
	status = strings.TrimSpace(status)
	var rows *sql.Rows
	var err error
	switch {
	case allWorkspaces && status == "":
		rows, err = db.Query(recommendationSelectSQL()+` ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, limit)
	case allWorkspaces:
		rows, err = db.Query(recommendationSelectSQL()+` WHERE r.status=? ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, status, limit)
	case status == "":
		rows, err = db.Query(recommendationSelectSQL()+` WHERE r.workspace_id=? ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, workspaceID, limit)
	default:
		rows, err = db.Query(recommendationSelectSQL()+` WHERE r.workspace_id=? AND r.status=? ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, workspaceID, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SelfImprovementRecommendationRow
	for rows.Next() {
		rec, err := scanSelfImprovementRecommendation(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func GetSelfImprovementRecommendation(db *sql.DB, id string) (SelfImprovementRecommendationRow, error) {
	return GetSelfImprovementRecommendationFrom(db, id)
}

func GetSelfImprovementRecommendationFrom(q querier, id string) (SelfImprovementRecommendationRow, error) {
	row := q.QueryRow(recommendationSelectSQL()+` WHERE r.id=?`, strings.TrimSpace(id))
	rec, err := scanSelfImprovementRecommendation(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementRecommendationRow{}, &ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	return rec, err
}

func UpsertSelfImprovementClarificationRow(q sqlExec, recommendationID, author, body string) error {
	recommendationID = strings.TrimSpace(recommendationID)
	body = strings.TrimSpace(body)
	if recommendationID == "" {
		return &ErrValidation{Msg: "recommendation id is required"}
	}
	if body == "" {
		return &ErrValidation{Msg: "clarification body is required"}
	}
	_, err := q.Exec(
		`INSERT INTO self_improvement_recommendation_clarifications (
			recommendation_id, author, body
		) VALUES (?,?,?)
		ON CONFLICT(recommendation_id) DO UPDATE SET
			author=excluded.author,
			body=excluded.body,
			updated_at=datetime('now')`,
		recommendationID, strings.TrimSpace(author), body,
	)
	return err
}

func UpdateSelfImprovementRecommendationStatusRow(q sqlExec, id, status string) error {
	return UpdateSelfImprovementRecommendationDecisionRow(q, id, status, "")
}

func UpdateSelfImprovementRecommendationDecisionRow(q sqlExec, id, status, reason string) error {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	if id == "" {
		return &ErrValidation{Msg: "recommendation id is required"}
	}
	res, err := q.Exec(`UPDATE self_improvement_recommendations SET status=?, decision_reason=?, updated_at=datetime('now') WHERE id=?`, status, strings.TrimSpace(reason), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	return nil
}

func UpdateSelfImprovementRecommendationStatusErrorRow(q sqlExec, id, status, message string) error {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	if id == "" {
		return &ErrValidation{Msg: "recommendation id is required"}
	}
	res, err := q.Exec(`UPDATE self_improvement_recommendations SET status=?, error=?, updated_at=datetime('now') WHERE id=?`, status, strings.TrimSpace(message), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("recommendation %q not found", id)}
	}
	return nil
}

func UpdateSelfImprovementFeedbackStatusRow(q sqlExec, id int64, status string) error {
	if id <= 0 {
		return &ErrValidation{Msg: "feedback id is required"}
	}
	res, err := q.Exec(`UPDATE self_improvement_feedback SET status=? WHERE id=?`, strings.TrimSpace(status), id)
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
	return nil
}

func readSelfImprovementCatalogVersion(q querier, targetType, versionID string) (fleet.CatalogVersion, error) {
	var version fleet.CatalogVersion
	var err error
	switch targetType {
	case "prompt":
		err = q.QueryRow(`
			SELECT id, prompt_id, version_number, state, description, content, source_type, source_ref, author, changelog,
			       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM prompt_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
				&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
				&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
	case "skill":
		err = q.QueryRow(`
			SELECT id, skill_id, version_number, state, prompt, source_type, source_ref, author, changelog,
			       COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM skill_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Prompt,
				&version.SourceType, &version.SourceRef, &version.Author, &version.Changelog,
				&version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
	case "guardrail":
		var enabled int
		err = q.QueryRow(`
			SELECT id, guardrail_id, version_number, state, description, content, enabled, position, source_type, source_ref,
			       author, changelog, COALESCE(base_version_id, ''), body_hash, created_at, COALESCE(published_at, '')
			FROM guardrail_versions
			WHERE id=?`, versionID).
			Scan(&version.ID, &version.AssetID, &version.Version, &version.State, &version.Description, &version.Content,
				&enabled, &version.Position, &version.SourceType, &version.SourceRef,
				&version.Author, &version.Changelog, &version.BaseVersionID, &version.BodyHash, &version.CreatedAt, &version.PublishedAt)
		version.Enabled = enabled != 0
	default:
		return fleet.CatalogVersion{}, &ErrValidation{Msg: fmt.Sprintf("recommendation target type %q is not proposal-convertible", targetType)}
	}
	if err != nil {
		return fleet.CatalogVersion{}, versionReadErr(targetType, versionID, err)
	}
	return version, nil
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
			WHERE feedback_event_id=?
			  AND status <> 'rejected'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM self_improvement_proposal_bundles b
			      WHERE b.recommendation_id = self_improvement_recommendations.id
			        AND b.status IN ('published', 'resolved', 'discarded')
			  )`,
		"failed", strings.TrimSpace(cause), id,
	)
	return err
}

func getSelfImprovementRecommendationByFeedback(db *sql.DB, workspaceID string, feedbackID int64) (SelfImprovementRecommendationRow, error) {
	row := db.QueryRow(recommendationSelectSQL()+` WHERE r.workspace_id=? AND r.feedback_event_id=?`, workspaceID, feedbackID)
	rec, err := scanSelfImprovementRecommendation(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return SelfImprovementRecommendationRow{}, &ErrNotFound{Msg: fmt.Sprintf("recommendation for feedback %d not found", feedbackID)}
	}
	return rec, err
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
		&ev.GitHubReviewCommentID, &ev.GitHubParentCommentID, &ev.GitHubPullRequestReviewID,
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
		r.proposed_patch, r.proposed_new_body, r.analyzer_prompt_ref,
		r.analyzer_prompt_version_id, r.structured_output, r.error, r.decision_reason, r.created_at, r.updated_at,
		f.id, f.workspace_id, f.repo_owner, f.repo_name, f.source_type, f.github_comment_id, f.github_review_id,
		f.github_review_comment_id, f.github_parent_comment_id, f.github_pull_request_review_id,
		f.github_delivery_id, f.source_url, f.author_login, f.author_authorized, f.issue_number, f.pr_number,
		f.raw_body, f.tag, f.file_path, f.line, f.side, f.diff_hunk, f.commit_sha, f.github_created_at,
		f.github_updated_at, f.ingested_at, f.linked_span_id, f.linked_event_id, f.linked_agent_id,
		f.linked_agent_name, f.linked_prompt_version_id, f.linked_skill_version_ids,
		f.linked_guardrail_version_ids, f.link_confidence, f.link_diagnostics, f.status,
		COALESCE(c.recommendation_id, ''), COALESCE(c.author, ''), COALESCE(c.body, ''),
		COALESCE(c.created_at, ''), COALESCE(c.updated_at, '')
	FROM self_improvement_recommendations r
	JOIN self_improvement_feedback f ON f.id = r.feedback_event_id
	LEFT JOIN self_improvement_recommendation_clarifications c ON c.recommendation_id = r.id`
}

func scanSelfImprovementRecommendation(row selfImprovementScanner, includeFeedback bool) (SelfImprovementRecommendationRow, error) {
	var rec SelfImprovementRecommendationRow
	var feedback SelfImprovementFeedback
	var clarification SelfImprovementClarificationRow
	var evidenceIDs, evidenceURLs, structured string
	var authorized int
	var skillIDs, guardrailIDs string
	if err := row.Scan(
		&rec.ID, &rec.WorkspaceID, &rec.FeedbackEventID, &rec.Type, &rec.Status, &rec.Confidence, &rec.Risk,
		&rec.Finding, &rec.NormalizedLesson, &rec.Rationale, &evidenceIDs, &evidenceURLs,
		&rec.AttributionConfidence, &rec.TargetAssetType, &rec.TargetAssetID, &rec.TargetBaseVersionID,
		&rec.ProposedPatch, &rec.ProposedNewBody, &rec.AnalyzerPromptRef,
		&rec.AnalyzerPromptVersionID, &structured, &rec.Error, &rec.DecisionReason, &rec.CreatedAt, &rec.UpdatedAt,
		&feedback.ID, &feedback.WorkspaceID, &feedback.RepoOwner, &feedback.RepoName, &feedback.SourceType,
		&feedback.GitHubCommentID, &feedback.GitHubReviewID, &feedback.GitHubReviewCommentID,
		&feedback.GitHubParentCommentID, &feedback.GitHubPullRequestReviewID,
		&feedback.GitHubDeliveryID, &feedback.SourceURL,
		&feedback.AuthorLogin, &authorized, &feedback.IssueNumber, &feedback.PRNumber, &feedback.RawBody,
		&feedback.Tag, &feedback.FilePath, &feedback.Line, &feedback.Side, &feedback.DiffHunk,
		&feedback.CommitSHA, &feedback.GitHubCreatedAt, &feedback.GitHubUpdatedAt, &feedback.IngestedAt,
		&feedback.LinkedSpanID, &feedback.LinkedEventID, &feedback.LinkedAgentID, &feedback.LinkedAgentName,
		&feedback.LinkedPromptVersionID, &skillIDs, &guardrailIDs, &feedback.LinkConfidence,
		&feedback.LinkDiagnostics, &feedback.Status, &clarification.RecommendationID, &clarification.Author,
		&clarification.Body, &clarification.CreatedAt, &clarification.UpdatedAt,
	); err != nil {
		return SelfImprovementRecommendationRow{}, err
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
	if clarification.RecommendationID != "" {
		rec.Clarification = &clarification
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

func selfImprovementAllWorkspaces(workspace string) bool {
	return strings.TrimSpace(workspace) == SelfImprovementAllWorkspaces
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

func readSkill(db querier, ref string) (fleet.Skill, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Skill{}, &ErrValidation{Msg: "skill id is required"}
	}
	var skill fleet.Skill
	err := db.QueryRow(`
		SELECT s.ref, COALESCE(s.workspace_id, ''), COALESCE(s.repo, ''), s.name, s.prompt,
		       COALESCE(sv.id, ''), COALESCE(sv.version_number, 0)
		FROM skills s
		LEFT JOIN skill_versions sv ON sv.id = s.current_version_id
		WHERE s.id=? OR s.ref=?`, ref, ref).
		Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt, &skill.VersionID, &skill.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, &ErrNotFound{Msg: fmt.Sprintf("skill %q not found", ref)}
	}
	if err != nil {
		return fleet.Skill{}, fmt.Errorf("store: read skill %s: %w", ref, err)
	}
	return skill, nil
}
