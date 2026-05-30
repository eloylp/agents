package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
)

const (
	FeedbackStatusNew     = "new"
	FeedbackStatusIgnored = "ignored"
	FeedbackTag           = "/agents improve"
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

func (s *Store) UpsertSelfImprovementFeedback(in SelfImprovementFeedbackInput) (SelfImprovementFeedback, error) {
	return UpsertSelfImprovementFeedback(s.db, in)
}

func (s *Store) IgnoreSelfImprovementFeedback(in SelfImprovementFeedbackInput) (bool, error) {
	return IgnoreSelfImprovementFeedback(s.db, in)
}

func (s *Store) ListSelfImprovementFeedback(workspace, status string, limit int) ([]SelfImprovementFeedback, error) {
	return ListSelfImprovementFeedback(s.db, workspace, status, limit)
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
