package observe

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// RunAttributionArtifactInput describes one GitHub artifact that carries
// valid signed agent attribution metadata. Only artifacts whose metadata
// passes signature verification and context matching are stored.
type RunAttributionArtifactInput struct {
	WorkspaceID           string
	RepoOwner             string
	RepoName              string
	IssueOrPRNumber       int
	SourceType            string // "issue_comment", "pull_request_review", "pull_request_review_comment"
	GitHubCommentID       int64  // issue_comment or pull_request_review_comment id
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

// RunAttributionArtifact is a stored GitHub artifact→span mapping.
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

// CaptureArtifact inspects body and commitMessage for valid signed agent
// attribution metadata and, if found, upserts a run_attribution_artifacts row.
// It is called for every incoming GitHub comment/review, not only for those
// containing /agents improve, so the daemon can later resolve ancestry for
// inline feedback that does not carry its own metadata.
//
// Invalid, unsigned, malformed, foreign-instance, or wrong-context metadata
// is logged and ignored; it is never stored as an authoritative artifact.
func (s *Store) CaptureArtifact(in RunAttributionArtifactInput, body, commitMessage string) {
	if s.db == nil {
		return
	}
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	q := AttributionQuery{
		Body:            body,
		CommitMessage:   commitMessage,
		WorkspaceID:     workspaceID,
		RepoOwner:       in.RepoOwner,
		RepoName:        in.RepoName,
		IssueOrPRNumber: in.IssueOrPRNumber,
	}
	metas := s.extractAttributionMetadata(q)
	if len(metas) == 0 {
		return
	}
	for _, candidate := range metas {
		meta := candidate.meta
		if err := workflow.VerifyPublicRunAttribution(meta, s.attributionVerifier.SigningSecret, s.attributionVerifier.InstanceID); err != nil {
			log.Printf("observe: artifact capture: ignore %s: %v", candidate.source, err)
			continue
		}
		if err := attributionMetadataMatchesQuery(meta, workspaceID, q); err != nil {
			log.Printf("observe: artifact capture: ignore %s: %v", candidate.source, err)
			continue
		}
		metaJSON := ""
		if b, err := json.Marshal(meta); err == nil {
			metaJSON = string(b)
		}
		artifact := in
		artifact.WorkspaceID = workspaceID
		artifact.SpanID = meta.SpanID
		artifact.MetadataJSON = metaJSON
		if err := s.upsertRunAttributionArtifact(artifact); err != nil {
			log.Printf("observe: artifact capture: upsert %s span=%s: %v", candidate.source, meta.SpanID, err)
		}
	}
}

func (s *Store) upsertRunAttributionArtifact(in RunAttributionArtifactInput) error {
	workspaceID := fleet.NormalizeWorkspaceID(in.WorkspaceID)
	_, err := s.db.Exec(
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

// runAttributionArtifactByCommentID looks up an artifact by issue_comment or
// pull_request_review_comment github_comment_id.
func (s *Store) runAttributionArtifactByCommentID(workspaceID, repoOwner, repoName, sourceType string, commentID int64) (RunAttributionArtifact, bool) {
	if s.db == nil || commentID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := s.db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND source_type=? AND github_comment_id=?
		LIMIT 1`,
		workspaceID, repoOwner, repoName, sourceType, commentID,
	)
	a, err := scanArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// runAttributionArtifactByReviewCommentID looks up an artifact by
// pull_request_review_comment id (github_review_comment_id column).
func (s *Store) runAttributionArtifactByReviewCommentID(workspaceID, repoOwner, repoName string, reviewCommentID int64) (RunAttributionArtifact, bool) {
	if s.db == nil || reviewCommentID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := s.db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND github_review_comment_id=?
		LIMIT 1`,
		workspaceID, repoOwner, repoName, reviewCommentID,
	)
	a, err := scanArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// runAttributionArtifactByReviewID looks up an artifact by pull_request_review id.
func (s *Store) runAttributionArtifactByReviewID(workspaceID, repoOwner, repoName string, reviewID int64) (RunAttributionArtifact, bool) {
	if s.db == nil || reviewID == 0 {
		return RunAttributionArtifact{}, false
	}
	row := s.db.QueryRow(
		`SELECT id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND github_review_id=? AND github_review_comment_id=0
		LIMIT 1`,
		workspaceID, repoOwner, repoName, reviewID,
	)
	a, err := scanArtifact(row)
	if err != nil {
		return RunAttributionArtifact{}, false
	}
	return a, true
}

// runAttributionArtifactsByPRContext returns artifacts for the given PR with
// optional file_path and commit_sha filters. Used for conservative fallback
// when parent/review ancestry is not available. Returns multiple matches so
// the caller can detect ambiguity.
func (s *Store) runAttributionArtifactsByPRContext(workspaceID, repoOwner, repoName string, prNumber int, filePath, commitSHA string) []RunAttributionArtifact {
	if s.db == nil || prNumber == 0 {
		return nil
	}
	filePath = strings.TrimSpace(filePath)
	commitSHA = strings.TrimSpace(commitSHA)
	rows, err := s.db.Query(
		`SELECT id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, github_comment_id, github_review_id, github_review_comment_id,
			github_parent_comment_id, github_delivery_id, source_url, author_login,
			file_path, line, side, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at
		FROM run_attribution_artifacts
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND issue_or_pr_number=?
		  AND (?='' OR file_path=?)
		  AND (?='' OR commit_sha=?)
		ORDER BY observed_at DESC`,
		workspaceID, repoOwner, repoName, prNumber,
		filePath, filePath,
		commitSHA, commitSHA,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RunAttributionArtifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err == nil {
			out = append(out, a)
		}
	}
	return out
}

type artifactScanner interface {
	Scan(dest ...any) error
}

func scanArtifact(row artifactScanner) (RunAttributionArtifact, error) {
	var a RunAttributionArtifact
	err := row.Scan(
		&a.ID, &a.WorkspaceID, &a.RepoOwner, &a.RepoName, &a.IssueOrPRNumber,
		&a.SourceType, &a.GitHubCommentID, &a.GitHubReviewID, &a.GitHubReviewCommentID,
		&a.GitHubParentCommentID, &a.GitHubDeliveryID, &a.SourceURL, &a.AuthorLogin,
		&a.FilePath, &a.Line, &a.Side, &a.CommitSHA, &a.SpanID, &a.MetadataJSON,
		&a.GitHubCreatedAt, &a.GitHubUpdatedAt, &a.ObservedAt,
	)
	if err != nil {
		return RunAttributionArtifact{}, fmt.Errorf("scan artifact: %w", err)
	}
	return a, nil
}
