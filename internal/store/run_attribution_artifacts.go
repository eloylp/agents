package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
)

// RunAttributionArtifactInput describes one GitHub artifact that carries
// valid signed agent attribution metadata.
type RunAttributionArtifactInput struct {
	WorkspaceID           string
	RepoOwner             string
	RepoName              string
	IssueOrPRNumber       int
	SourceType            string // "issue_comment", "pull_request_review", "pull_request_review_comment", "commit"
	GitHubCommentID       int64  // issue_comment id
	GitHubReviewID        int64  // pull_request_review id
	GitHubReviewCommentID int64  // pull_request_review_comment id (distinguished from CommentID)
	GitHubParentCommentID int64  // in_reply_to_id from PR review comment
	GitHubDeliveryID      string
	SourceURL             string
	AuthorLogin           string
	FilePath              string
	Line                  int
	Side                  string
	CommitSHA             string
	SpanID                string
	MetadataJSON          string
	GitHubCreatedAt       *time.Time
	GitHubUpdatedAt       *time.Time
}

// RunAttributionArtifact is a stored GitHub artifact-to-span mapping.
type RunAttributionArtifact struct {
	ID                    int64
	WorkspaceID           string
	RepoOwner             string
	RepoName              string
	IssueOrPRNumber       int
	SourceType            string
	GitHubCommentID       int64
	GitHubReviewID        int64
	GitHubReviewCommentID int64
	GitHubParentCommentID int64
	GitHubDeliveryID      string
	SourceURL             string
	AuthorLogin           string
	FilePath              string
	Line                  int
	Side                  string
	CommitSHA             string
	SpanID                string
	MetadataJSON          string
	GitHubCreatedAt       *time.Time
	GitHubUpdatedAt       *time.Time
	ObservedAt            time.Time
}

func (s *Store) UpsertRunAttributionArtifact(in RunAttributionArtifactInput) error {
	return UpsertRunAttributionArtifact(s.db, in)
}

func (s *Store) RunAttributionArtifactByCommentID(workspaceID, repoOwner, repoName, sourceType string, commentID int64) (RunAttributionArtifact, bool) {
	return RunAttributionArtifactByCommentID(s.db, workspaceID, repoOwner, repoName, sourceType, commentID)
}

func (s *Store) RunAttributionArtifactByReviewCommentID(workspaceID, repoOwner, repoName string, reviewCommentID int64) (RunAttributionArtifact, bool) {
	return RunAttributionArtifactByReviewCommentID(s.db, workspaceID, repoOwner, repoName, reviewCommentID)
}

func (s *Store) RunAttributionArtifactByReviewID(workspaceID, repoOwner, repoName string, reviewID int64) (RunAttributionArtifact, bool) {
	return RunAttributionArtifactByReviewID(s.db, workspaceID, repoOwner, repoName, reviewID)
}

func (s *Store) RunAttributionArtifactByCommitSHA(workspaceID, repoOwner, repoName, commitSHA string) (RunAttributionArtifact, bool) {
	return RunAttributionArtifactByCommitSHA(s.db, workspaceID, repoOwner, repoName, commitSHA)
}

func (s *Store) RunAttributionArtifactsByPRContext(workspaceID, repoOwner, repoName string, prNumber int, filePath, commitSHA string) []RunAttributionArtifact {
	return RunAttributionArtifactsByPRContext(s.db, workspaceID, repoOwner, repoName, prNumber, filePath, commitSHA)
}

func UpsertRunAttributionArtifact(db sqlExec, in RunAttributionArtifactInput) error {
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	_, err := db.Exec(
		`INSERT INTO run_attribution_artifacts (
			workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`,
		workspaceID, strings.TrimSpace(in.RepoOwner), strings.TrimSpace(in.RepoName), in.IssueOrPRNumber,
		strings.TrimSpace(in.SourceType), in.GitHubCommentID, in.GitHubReviewID, in.GitHubReviewCommentID,
		in.GitHubParentCommentID, strings.TrimSpace(in.GitHubDeliveryID), strings.TrimSpace(in.SourceURL),
		strings.TrimSpace(in.AuthorLogin), strings.TrimSpace(in.FilePath), in.Line, strings.TrimSpace(in.Side),
		strings.TrimSpace(in.CommitSHA), strings.TrimSpace(in.SpanID), in.MetadataJSON,
		in.GitHubCreatedAt, in.GitHubUpdatedAt,
	)
	return err
}

// RunAttributionArtifactByCommentID looks up an artifact by issue_comment or
// pull_request_review_comment github_comment_id.
func RunAttributionArtifactByCommentID(db querier, workspaceID, repoOwner, repoName, sourceType string, commentID int64) (RunAttributionArtifact, bool) {
	if db == nil || commentID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := db.QueryRow(
		runAttributionArtifactSelect+`
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND source_type=? AND github_comment_id=?
		LIMIT 1`,
		fleet.NormalizeWorkspaceID(workspaceID), strings.TrimSpace(repoOwner), strings.TrimSpace(repoName), strings.TrimSpace(sourceType), commentID,
	)
	a, err := scanRunAttributionArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// RunAttributionArtifactByReviewCommentID looks up an artifact by
// pull_request_review_comment id (github_review_comment_id column).
func RunAttributionArtifactByReviewCommentID(db querier, workspaceID, repoOwner, repoName string, reviewCommentID int64) (RunAttributionArtifact, bool) {
	if db == nil || reviewCommentID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := db.QueryRow(
		runAttributionArtifactSelect+`
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND github_review_comment_id=?
		LIMIT 1`,
		fleet.NormalizeWorkspaceID(workspaceID), strings.TrimSpace(repoOwner), strings.TrimSpace(repoName), reviewCommentID,
	)
	a, err := scanRunAttributionArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// RunAttributionArtifactByReviewID looks up an artifact by pull_request_review id.
func RunAttributionArtifactByReviewID(db querier, workspaceID, repoOwner, repoName string, reviewID int64) (RunAttributionArtifact, bool) {
	if db == nil || reviewID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := db.QueryRow(
		runAttributionArtifactSelect+`
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND github_review_id=? AND github_review_comment_id=0
		LIMIT 1`,
		fleet.NormalizeWorkspaceID(workspaceID), strings.TrimSpace(repoOwner), strings.TrimSpace(repoName), reviewID,
	)
	a, err := scanRunAttributionArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// RunAttributionArtifactByCommitSHA looks up a signed commit artifact by the
// commit SHA GitHub reports for a diff-line review comment.
func RunAttributionArtifactByCommitSHA(db querier, workspaceID, repoOwner, repoName, commitSHA string) (RunAttributionArtifact, bool) {
	commitSHA = strings.TrimSpace(commitSHA)
	if db == nil || commitSHA == "" {
		return RunAttributionArtifact{}, false
	}
	row := db.QueryRow(
		runAttributionArtifactSelect+`
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND source_type='commit' AND commit_sha=?
		LIMIT 1`,
		fleet.NormalizeWorkspaceID(workspaceID), strings.TrimSpace(repoOwner), strings.TrimSpace(repoName), commitSHA,
	)
	a, err := scanRunAttributionArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// RunAttributionArtifactsByPRContext returns artifacts for the given PR with
// optional file_path and commit_sha filters.
func RunAttributionArtifactsByPRContext(db querier, workspaceID, repoOwner, repoName string, prNumber int, filePath, commitSHA string) []RunAttributionArtifact {
	if db == nil || prNumber == 0 {
		return nil
	}
	filePath = strings.TrimSpace(filePath)
	commitSHA = strings.TrimSpace(commitSHA)
	rows, err := db.Query(
		runAttributionArtifactSelect+`
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND issue_or_pr_number=?
		  AND (?='' OR file_path=?)
		  AND (?='' OR commit_sha=?)
		ORDER BY observed_at DESC`,
		fleet.NormalizeWorkspaceID(workspaceID), strings.TrimSpace(repoOwner), strings.TrimSpace(repoName), prNumber,
		filePath, filePath,
		commitSHA, commitSHA,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RunAttributionArtifact
	for rows.Next() {
		a, err := scanRunAttributionArtifact(rows)
		if err == nil {
			out = append(out, a)
		}
	}
	return out
}

const runAttributionArtifactSelect = `SELECT id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at`

type runAttributionArtifactScanner interface {
	Scan(dest ...any) error
}

func scanRunAttributionArtifact(row runAttributionArtifactScanner) (RunAttributionArtifact, error) {
	var a RunAttributionArtifact
	var githubCreatedAt, githubUpdatedAt, observedAt sql.NullString
	err := row.Scan(
		&a.ID, &a.WorkspaceID, &a.RepoOwner, &a.RepoName, &a.IssueOrPRNumber,
		&a.SourceType, &a.GitHubCommentID, &a.GitHubReviewID, &a.GitHubReviewCommentID,
		&a.GitHubParentCommentID, &a.GitHubDeliveryID, &a.SourceURL, &a.AuthorLogin,
		&a.FilePath, &a.Line, &a.Side, &a.CommitSHA, &a.SpanID, &a.MetadataJSON,
		&githubCreatedAt, &githubUpdatedAt, &observedAt,
	)
	if err != nil {
		return RunAttributionArtifact{}, fmt.Errorf("scan run attribution artifact: %w", err)
	}
	a.GitHubCreatedAt = parseRunAttributionArtifactTimePtr(githubCreatedAt)
	a.GitHubUpdatedAt = parseRunAttributionArtifactTimePtr(githubUpdatedAt)
	if t := parseRunAttributionArtifactTimePtr(observedAt); t != nil {
		a.ObservedAt = *t
	}
	return a, nil
}

func parseRunAttributionArtifactTimePtr(ns sql.NullString) *time.Time {
	value := strings.TrimSpace(ns.String)
	if !ns.Valid || value == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.DateTime,
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 -0700",
		"2006-01-02 15:04:05.999999999 -0700 -0700",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
