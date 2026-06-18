package observe

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	storepkg "github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

type AttributionSnapshot struct {
	WorkspaceID         string    `json:"workspace"`
	RepoOwner           string    `json:"repo_owner"`
	RepoName            string    `json:"repo_name"`
	IssueOrPRNumber     int       `json:"issue_or_pr_number"`
	EventID             string    `json:"event_id"`
	EventQueueID        int64     `json:"event_queue_id"`
	SpanID              string    `json:"span_id"`
	AgentID             string    `json:"agent_id,omitempty"`
	AgentName           string    `json:"agent_name"`
	BackendID           string    `json:"backend_id,omitempty"`
	BackendName         string    `json:"backend_name"`
	PromptVersionID     string    `json:"prompt_version_id,omitempty"`
	PromptRef           string    `json:"prompt_ref,omitempty"`
	SkillVersionIDs     []string  `json:"skill_version_ids,omitempty"`
	GuardrailVersionIDs []string  `json:"guardrail_version_ids,omitempty"`
	HeadSHA             string    `json:"head_sha,omitempty"`
	Branch              string    `json:"branch,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type AttributionQuery struct {
	Body            string
	CommitMessage   string
	WorkspaceID     string
	RepoOwner       string
	RepoName        string
	IssueOrPRNumber int
	HeadSHA         string
	At              time.Time
	Window          time.Duration

	// Artifact ancestry fields for artifact-chain resolution.
	// Set these when resolving attribution for a specific GitHub artifact.
	CommentID           int64  // github_comment_id (issue_comment)
	ReviewID            int64  // github_review_id (pull_request_review)
	ReviewCommentID     int64  // github_review_comment_id (pull_request_review_comment id)
	InReplyToID         int64  // in_reply_to_id from PR review comment
	PullRequestReviewID int64  // pull_request_review_id from PR review comment
	FilePath            string // file path for PR review comment context
}

type AttributionResolution struct {
	Confidence string               `json:"confidence"`
	Mode       string               `json:"mode,omitempty"`
	Snapshot   *AttributionSnapshot `json:"snapshot,omitempty"`
	Diagnostic string               `json:"diagnostic,omitempty"`
}

const (
	AttributionExact      = "exact"
	AttributionInferred   = "inferred"
	AttributionUnresolved = "unresolved"
)

// Attribution mode values describe how the span was resolved.
const (
	AttributionModeDirect            = "direct"
	AttributionModeArtifactComment   = "artifact_comment"
	AttributionModeArtifactParent    = "artifact_parent_comment"
	AttributionModeArtifactReview    = "artifact_review"
	AttributionModeArtifactPRContext = "artifact_pr_context"
	AttributionModeCommitArtifact    = "commit_artifact"
	AttributionModeCommitTrailer     = "commit_trailer"
	AttributionModeInferred          = "inferred"
	AttributionModeUnresolved        = "unresolved"
)

// selfImprovementInternalAgentNames lists internal analyst agent names that
// should not be targeted by loose time-window inference. Exact attribution via
// signed metadata or artifact ancestry is still accepted for these agents.
var selfImprovementInternalAgentNames = map[string]bool{
	"internal-catalog-analyst": true,
}

var agentsRunCommentRE = regexp.MustCompile(`(?s)<!--\s*agents-run:\s*(\{.*?\})\s*-->`)

func (s *Store) ResolveRunAttribution(q AttributionQuery) AttributionResolution {
	workspaceID := fleet.NormalizeWorkspaceID(q.WorkspaceID)

	// 1. Direct signed metadata in the body (hidden comment or commit trailer).
	if resolved, ok := s.resolveExactAttribution(workspaceID, q); ok {
		return resolved
	}

	// 2–6. Artifact-chain walk for PR review comments and related artifacts.
	if resolved, ok := s.resolveArtifactAttribution(workspaceID, q); ok {
		return resolved
	}

	// 7. Conservative time-window inference; skip internal analyst agent runs.
	matches := s.inferRunAttributions(workspaceID, q)
	switch len(matches) {
	case 0:
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: AttributionModeUnresolved, Diagnostic: "no matching run attribution snapshot"}
	case 1:
		return AttributionResolution{Confidence: AttributionInferred, Mode: AttributionModeInferred, Snapshot: &matches[0]}
	default:
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: AttributionModeUnresolved, Diagnostic: fmt.Sprintf("ambiguous run attribution: %d matches", len(matches))}
	}
}

// resolveArtifactAttribution walks the GitHub artifact ownership chain to
// resolve attribution without requiring the human's comment to carry metadata.
// Returns (resolution, true) when a definitive result is reached (including
// ambiguous or unresolved results that should short-circuit further attempts).
func (s *Store) resolveArtifactAttribution(workspaceID string, q AttributionQuery) (AttributionResolution, bool) {
	if s.db == nil {
		return AttributionResolution{}, false
	}
	owner := strings.TrimSpace(q.RepoOwner)
	name := strings.TrimSpace(q.RepoName)
	if owner == "" || name == "" {
		return AttributionResolution{}, false
	}

	var missingCommitArtifact bool
	if q.ReviewCommentID > 0 && strings.TrimSpace(q.HeadSHA) != "" {
		if a, ok := s.store.RunAttributionArtifactByCommitSHA(workspaceID, owner, name, q.HeadSHA); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("review_comment_id", q.ReviewCommentID).
				Str("head_sha", strings.TrimSpace(q.HeadSHA)).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution commit artifact hit")
			return s.artifactResolution(a, AttributionModeCommitArtifact)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int("number", q.IssueOrPRNumber).
			Int64("review_comment_id", q.ReviewCommentID).
			Int64("pull_request_review_id", q.PullRequestReviewID).
			Str("head_sha", strings.TrimSpace(q.HeadSHA)).
			Str("file_path", strings.TrimSpace(q.FilePath)).
			Msg("attribution commit artifact miss")
		missingCommitArtifact = true
	}

	// Step 3: Parent review comment lookup via in_reply_to_id.
	if q.InReplyToID > 0 {
		if a, ok := s.store.RunAttributionArtifactByReviewCommentID(workspaceID, owner, name, q.InReplyToID); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("parent_review_comment_id", q.InReplyToID).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution parent review comment artifact hit")
			return s.artifactResolution(a, AttributionModeArtifactParent)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int64("parent_review_comment_id", q.InReplyToID).
			Msg("attribution parent review comment artifact miss")
	}

	// Step 4: Owning review lookup via pull_request_review_id.
	if q.PullRequestReviewID > 0 {
		if a, ok := s.store.RunAttributionArtifactByReviewID(workspaceID, owner, name, q.PullRequestReviewID); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("pull_request_review_id", q.PullRequestReviewID).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution review artifact hit")
			return s.artifactResolution(a, AttributionModeArtifactReview)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int64("pull_request_review_id", q.PullRequestReviewID).
			Msg("attribution review artifact miss")
	}

	// Step 5: ReviewID direct lookup (for pull_request_review feedback).
	if q.ReviewID > 0 {
		if a, ok := s.store.RunAttributionArtifactByReviewID(workspaceID, owner, name, q.ReviewID); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("review_id", q.ReviewID).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution direct review artifact hit")
			return s.artifactResolution(a, AttributionModeArtifactReview)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int64("review_id", q.ReviewID).
			Msg("attribution direct review artifact miss")
	}

	// Step 6: issue_comment artifact lookup.
	if q.CommentID > 0 {
		if a, ok := s.store.RunAttributionArtifactByCommentID(workspaceID, owner, name, "issue_comment", q.CommentID); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("comment_id", q.CommentID).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution issue comment artifact hit")
			return s.artifactResolution(a, AttributionModeArtifactComment)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int64("comment_id", q.CommentID).
			Msg("attribution issue comment artifact miss")
	}

	// Step 6a: Direct PR review comment artifact lookup. This comes after
	// commit/parent/review ownership because a human diff-line feedback comment
	// may carry its own metadata while still targeting unsigned agent-authored
	// code; code ownership should win.
	if q.ReviewCommentID > 0 {
		if a, ok := s.store.RunAttributionArtifactByReviewCommentID(workspaceID, owner, name, q.ReviewCommentID); ok {
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int64("review_comment_id", q.ReviewCommentID).
				Int64("artifact_id", a.ID).
				Str("span_id", a.SpanID).
				Msg("attribution review comment artifact hit")
			return s.artifactResolution(a, AttributionModeArtifactComment)
		}
		s.logger.Debug().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int64("review_comment_id", q.ReviewCommentID).
			Msg("attribution review comment artifact miss")
	}

	if missingCommitArtifact {
		s.logger.Warn().
			Str("workspace", workspaceID).
			Str("repo_owner", owner).
			Str("repo_name", name).
			Int("number", q.IssueOrPRNumber).
			Int64("review_comment_id", q.ReviewCommentID).
			Str("head_sha", strings.TrimSpace(q.HeadSHA)).
			Str("diagnostic", "commented commit has no signed agent attribution").
			Msg("attribution unresolved after artifact walk")
		return AttributionResolution{
			Confidence: AttributionUnresolved,
			Mode:       AttributionModeCommitArtifact,
			Diagnostic: "commented commit has no signed agent attribution",
		}, true
	}

	// Step 7: Conservative PR/thread context lookup – only if it yields exactly
	// one artifact candidate with matching file + commit evidence.
	if q.IssueOrPRNumber > 0 && (strings.TrimSpace(q.FilePath) != "" || strings.TrimSpace(q.HeadSHA) != "") {
		candidates := s.store.RunAttributionArtifactsByPRContext(workspaceID, owner, name, q.IssueOrPRNumber, q.FilePath, q.HeadSHA)
		switch len(candidates) {
		case 0:
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int("number", q.IssueOrPRNumber).
				Str("file_path", strings.TrimSpace(q.FilePath)).
				Str("head_sha", strings.TrimSpace(q.HeadSHA)).
				Msg("attribution PR context artifact miss")
			// No artifact candidates; fall through to inference.
		case 1:
			s.logger.Debug().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int("number", q.IssueOrPRNumber).
				Str("file_path", strings.TrimSpace(q.FilePath)).
				Str("head_sha", strings.TrimSpace(q.HeadSHA)).
				Int64("artifact_id", candidates[0].ID).
				Str("span_id", candidates[0].SpanID).
				Msg("attribution PR context artifact hit")
			return s.artifactResolution(candidates[0], AttributionModeArtifactPRContext)
		default:
			s.logger.Warn().
				Str("workspace", workspaceID).
				Str("repo_owner", owner).
				Str("repo_name", name).
				Int("number", q.IssueOrPRNumber).
				Str("file_path", strings.TrimSpace(q.FilePath)).
				Str("head_sha", strings.TrimSpace(q.HeadSHA)).
				Int("candidates", len(candidates)).
				Msg("attribution PR context ambiguous")
			return AttributionResolution{
				Confidence: AttributionUnresolved,
				Mode:       AttributionModeArtifactPRContext,
				Diagnostic: fmt.Sprintf("ambiguous PR artifact context: %d candidates", len(candidates)),
			}, true
		}
	}

	return AttributionResolution{}, false
}

// artifactResolution loads the canonical run_attributions snapshot for the
// span referenced by artifact a and returns an AttributionResolution.
func (s *Store) artifactResolution(a RunAttributionArtifact, mode string) (AttributionResolution, bool) {
	snap, ok := s.runAttributionBySpan(a.WorkspaceID, a.SpanID)
	if !ok {
		s.logger.Warn().
			Str("mode", mode).
			Int64("artifact_id", a.ID).
			Str("workspace", a.WorkspaceID).
			Str("repo_owner", a.RepoOwner).
			Str("repo_name", a.RepoName).
			Str("source_type", a.SourceType).
			Str("span_id", a.SpanID).
			Msg("attribution artifact references unknown span")
		return AttributionResolution{
			Confidence: AttributionUnresolved,
			Mode:       mode,
			Diagnostic: fmt.Sprintf("artifact references unknown span %q", a.SpanID),
		}, true
	}
	s.logger.Debug().
		Str("mode", mode).
		Int64("artifact_id", a.ID).
		Str("workspace", a.WorkspaceID).
		Str("repo_owner", a.RepoOwner).
		Str("repo_name", a.RepoName).
		Str("span_id", a.SpanID).
		Str("agent", snap.AgentName).
		Str("prompt_version_id", snap.PromptVersionID).
		Strs("skill_version_ids", snap.SkillVersionIDs).
		Strs("guardrail_version_ids", snap.GuardrailVersionIDs).
		Msg("attribution resolved via artifact")
	return AttributionResolution{Confidence: AttributionExact, Mode: mode, Snapshot: &snap}, true
}

func (s *Store) resolveExactAttribution(workspaceID string, q AttributionQuery) (AttributionResolution, bool) {
	metas := s.extractAttributionMetadata(q)
	if len(metas) == 0 {
		if s.attributionVerifier.SigningSecret == "" {
			if spanID := spanIDFromLegacyCommitTrailers(q.CommitMessage); spanID != "" {
				if snap, ok := s.runAttributionBySpan(workspaceID, spanID); ok {
					return AttributionResolution{Confidence: AttributionExact, Mode: AttributionModeCommitTrailer, Snapshot: &snap}, true
				}
				return AttributionResolution{Confidence: AttributionUnresolved, Mode: AttributionModeCommitTrailer, Diagnostic: fmt.Sprintf("commit trailer names unknown span %q", spanID)}, true
			}
		}
		return AttributionResolution{}, false
	}
	var diagnostics []string
	var firstUnknownSpan string
	var firstExact *AttributionSnapshot
	for _, candidate := range metas {
		meta := candidate.meta
		if err := workflow.VerifyPublicRunAttribution(meta, s.attributionVerifier.SigningSecret, s.attributionVerifier.InstanceID); err != nil {
			diagnostic := fmt.Sprintf("%s: %v", candidate.source, err)
			log.Printf("observe: ignore run attribution metadata: %s", diagnostic)
			diagnostics = append(diagnostics, diagnostic)
			continue
		}
		if err := attributionMetadataMatchesQuery(meta, workspaceID, q); err != nil {
			diagnostic := fmt.Sprintf("%s: %v", candidate.source, err)
			log.Printf("observe: ignore run attribution metadata: %s", diagnostic)
			diagnostics = append(diagnostics, diagnostic)
			continue
		}
		if snap, ok := s.runAttributionBySpan(fleet.NormalizeWorkspaceID(meta.WorkspaceID), meta.SpanID); ok {
			if firstExact == nil {
				snapCopy := snap
				firstExact = &snapCopy
			}
			continue
		}
		if firstUnknownSpan == "" {
			firstUnknownSpan = fmt.Sprintf("%s names unknown span %q", candidate.source, meta.SpanID)
		}
	}
	if firstExact != nil {
		return AttributionResolution{Confidence: AttributionExact, Mode: AttributionModeDirect, Snapshot: firstExact}, true
	}
	if firstUnknownSpan != "" {
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: AttributionModeDirect, Diagnostic: firstUnknownSpan}, true
	}
	if len(diagnostics) > 0 {
		return AttributionResolution{Confidence: AttributionUnresolved, Mode: AttributionModeDirect, Diagnostic: "no valid signed attribution metadata: " + strings.Join(diagnostics, "; ")}, true
	}
	return AttributionResolution{}, false
}

type attributionMetadataCandidate struct {
	source string
	meta   workflow.PublicRunAttribution
}

func (s *Store) extractAttributionMetadata(q AttributionQuery) []attributionMetadataCandidate {
	var out []attributionMetadataCandidate
	for i, m := range agentsRunCommentRE.FindAllStringSubmatch(q.Body, -1) {
		if len(m) != 2 {
			continue
		}
		meta, err := workflow.DecodePublicRunAttribution([]byte(m[1]))
		if err != nil {
			diagnostic := fmt.Sprintf("hidden comment %d: malformed attribution metadata: %v", i+1, err)
			log.Printf("observe: ignore run attribution metadata: %s", diagnostic)
			continue
		}
		out = append(out, attributionMetadataCandidate{
			source: fmt.Sprintf("hidden comment %d", i+1),
			meta:   meta,
		})
	}
	for i, value := range signedCommitAttributionTrailers(q.CommitMessage) {
		meta, err := workflow.DecodeCommitAttributionTrailer(value)
		if err != nil {
			diagnostic := fmt.Sprintf("commit attribution trailer %d: malformed attribution metadata: %v", i+1, err)
			log.Printf("observe: ignore run attribution metadata: %s", diagnostic)
			continue
		}
		out = append(out, attributionMetadataCandidate{
			source: fmt.Sprintf("commit attribution trailer %d", i+1),
			meta:   meta,
		})
	}
	return out
}

func attributionMetadataMatchesQuery(meta workflow.PublicRunAttribution, workspaceID string, q AttributionQuery) error {
	metaWorkspace := fleet.NormalizeWorkspaceID(meta.WorkspaceID)
	if metaWorkspace != workspaceID {
		return fmt.Errorf("metadata workspace %q does not match query workspace %q", metaWorkspace, workspaceID)
	}
	if q.RepoOwner != "" || q.RepoName != "" {
		queryRepo := strings.ToLower(strings.Trim(strings.TrimSpace(q.RepoOwner)+"/"+strings.TrimSpace(q.RepoName), "/"))
		if meta.Repo != "" && meta.Repo != queryRepo {
			return fmt.Errorf("metadata repo %q does not match query repo %q", meta.Repo, queryRepo)
		}
	}
	if q.IssueOrPRNumber > 0 && meta.IssueOrPRNumber > 0 && meta.IssueOrPRNumber != q.IssueOrPRNumber {
		return fmt.Errorf("metadata issue/PR number %d does not match query number %d", meta.IssueOrPRNumber, q.IssueOrPRNumber)
	}
	return nil
}

func signedCommitAttributionTrailers(message string) []string {
	var out []string
	for _, line := range strings.Split(message, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "Agents-Attribution") {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func spanIDFromLegacyCommitTrailers(message string) string {
	var spanID string
	for _, line := range strings.Split(message, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "Agents-Run") {
			spanID = strings.TrimSpace(v)
		}
	}
	return spanID
}

func (s *Store) recordRunAttribution(in workflow.SpanInput, createdAt time.Time) {
	if s.db == nil || in.SpanID == "" {
		return
	}
	a := attributionSnapshotFromSpan(in, createdAt)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO run_attributions (
			span_id, workspace_id, repo_owner, repo_name, issue_or_pr_number,
			event_id, event_queue_id, agent_id, agent_name, backend_id, backend_name,
			prompt_version_id, prompt_ref, skill_version_ids, guardrail_version_ids,
			head_sha, branch, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.SpanID, a.WorkspaceID, a.RepoOwner, a.RepoName, a.IssueOrPRNumber,
		a.EventID, a.EventQueueID, a.AgentID, a.AgentName, a.BackendID, a.BackendName,
		nullString(a.PromptVersionID), a.PromptRef, strings.Join(a.SkillVersionIDs, ","), strings.Join(a.GuardrailVersionIDs, ","),
		a.HeadSHA, a.Branch, a.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		log.Printf("observe: persist run attribution %s: %v", in.SpanID, err)
	}
}

func attributionSnapshotFromSpan(in workflow.SpanInput, createdAt time.Time) AttributionSnapshot {
	a := in.Attribution
	owner, name := repoParts(in.Repo)
	out := AttributionSnapshot{
		WorkspaceID:         fleet.NormalizeWorkspaceID(firstNonEmpty(a.WorkspaceID, in.WorkspaceID)),
		RepoOwner:           firstNonEmpty(a.RepoOwner, owner),
		RepoName:            firstNonEmpty(a.RepoName, name),
		IssueOrPRNumber:     firstNonZero(a.IssueOrPRNumber, in.Number),
		EventID:             a.EventID,
		EventQueueID:        firstNonZero64(a.EventQueueID, in.EventQueueID),
		SpanID:              firstNonEmpty(a.SpanID, in.SpanID),
		AgentID:             a.AgentID,
		AgentName:           firstNonEmpty(a.AgentName, in.Agent),
		BackendID:           firstNonEmpty(a.BackendID, in.Backend),
		BackendName:         firstNonEmpty(a.BackendName, in.Backend),
		PromptVersionID:     firstNonEmpty(a.PromptVersionID, in.PromptVersionID),
		PromptRef:           a.PromptRef,
		SkillVersionIDs:     firstNonEmptyStrings(a.SkillVersionIDs, in.SkillVersionIDs),
		GuardrailVersionIDs: firstNonEmptyStrings(a.GuardrailVersionIDs, in.GuardrailVersionIDs),
		HeadSHA:             a.HeadSHA,
		Branch:              a.Branch,
		CreatedAt:           createdAt,
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now()
	}
	return out
}

func (s *Store) runAttributionBySpan(workspaceID, spanID string) (AttributionSnapshot, bool) {
	if s.db == nil {
		return AttributionSnapshot{}, false
	}
	row := s.db.QueryRow(`SELECT workspace_id, repo_owner, repo_name, issue_or_pr_number, event_id, event_queue_id, span_id, agent_id, agent_name, backend_id, backend_name, COALESCE(prompt_version_id, ''), prompt_ref, skill_version_ids, guardrail_version_ids, head_sha, branch, created_at FROM run_attributions WHERE workspace_id=? AND span_id=?`, workspaceID, spanID)
	snap, err := scanAttribution(row)
	return snap, err == nil
}

func (s *Store) inferRunAttributions(workspaceID string, q AttributionQuery) []AttributionSnapshot {
	if s.db == nil || q.RepoOwner == "" || q.RepoName == "" {
		return nil
	}
	if q.At.IsZero() {
		return nil
	}
	window := q.Window
	if window <= 0 {
		window = 24 * time.Hour
	}
	from := q.At.Add(-window)
	to := q.At.Add(window)
	rows, err := s.db.Query(
		`SELECT workspace_id, repo_owner, repo_name, issue_or_pr_number, event_id, event_queue_id, span_id, agent_id, agent_name, backend_id, backend_name, COALESCE(prompt_version_id, ''), prompt_ref, skill_version_ids, guardrail_version_ids, head_sha, branch, created_at
		FROM run_attributions
		WHERE workspace_id=? AND repo_owner=? AND repo_name=? AND issue_or_pr_number=?
		  AND (?='' OR head_sha=?)
		  AND (
			created_at BETWEEN ? AND ?
			OR replace(substr(created_at, 1, 19), 'T', ' ') BETWEEN ? AND ?
		  )
		ORDER BY replace(substr(created_at, 1, 19), 'T', ' ') DESC, created_at DESC`,
		workspaceID, q.RepoOwner, q.RepoName, q.IssueOrPRNumber,
		strings.TrimSpace(q.HeadSHA), strings.TrimSpace(q.HeadSHA),
		from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano),
		from.UTC().Format(time.DateTime), to.UTC().Format(time.DateTime),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AttributionSnapshot
	for rows.Next() {
		snap, err := scanAttribution(rows)
		if err != nil {
			continue
		}
		// Exclude internal analyst agent runs from loose inference; they are
		// only reachable via exact signed metadata or artifact ancestry.
		if selfImprovementInternalAgentNames[strings.TrimSpace(snap.AgentName)] {
			continue
		}
		out = append(out, snap)
	}
	return out
}

type attributionScanner interface {
	Scan(dest ...any) error
}

func scanAttribution(row attributionScanner) (AttributionSnapshot, error) {
	var snap AttributionSnapshot
	var skills, guardrails string
	var createdAt storepkg.SQLiteTime
	err := row.Scan(
		&snap.WorkspaceID, &snap.RepoOwner, &snap.RepoName, &snap.IssueOrPRNumber,
		&snap.EventID, &snap.EventQueueID, &snap.SpanID, &snap.AgentID, &snap.AgentName,
		&snap.BackendID, &snap.BackendName, &snap.PromptVersionID, &snap.PromptRef,
		&skills, &guardrails, &snap.HeadSHA, &snap.Branch, &createdAt,
	)
	if err != nil {
		return AttributionSnapshot{}, err
	}
	if err := createdAt.Err(); err != nil {
		return AttributionSnapshot{}, fmt.Errorf("scan attribution created_at: %w", err)
	}
	snap.CreatedAt = createdAt.OrZero()
	snap.SkillVersionIDs = splitList(skills)
	snap.GuardrailVersionIDs = splitList(guardrails)
	return snap, nil
}

func repoParts(repo string) (string, string) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", strings.TrimSpace(repo)
	}
	return strings.TrimSpace(owner), strings.TrimSpace(name)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: strings.TrimSpace(s) != ""}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonEmptyStrings(values ...[]string) []string {
	for _, v := range values {
		if len(v) > 0 {
			return append([]string(nil), v...)
		}
	}
	return nil
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func firstNonZero64(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}
