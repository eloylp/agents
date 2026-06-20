package observe

import (
	"encoding/json"

	"github.com/eloylp/agents/internal/fleet"
	storepkg "github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

type RunAttributionArtifactInput = storepkg.RunAttributionArtifactInput
type RunAttributionArtifact = storepkg.RunAttributionArtifact

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
			s.logger.Warn().
				Err(err).
				Str("workspace", workspaceID).
				Str("repo", q.repoFullName()).
				Int("number", q.IssueOrPRNumber).
				Str("source_type", candidate.source).
				Str("diagnostic", err.Error()).
				Msg("ignore run attribution artifact metadata")
			continue
		}
		if err := attributionMetadataMatchesQuery(meta, workspaceID, q); err != nil {
			s.logger.Warn().
				Err(err).
				Str("workspace", workspaceID).
				Str("repo", q.repoFullName()).
				Int("number", q.IssueOrPRNumber).
				Str("span_id", meta.SpanID).
				Str("source_type", candidate.source).
				Str("diagnostic", err.Error()).
				Msg("ignore run attribution artifact metadata")
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
		if err := s.store.UpsertRunAttributionArtifact(artifact); err != nil {
			s.logger.Error().Err(err).
				Str("workspace", artifact.WorkspaceID).
				Str("repo", q.repoFullName()).
				Int("number", artifact.IssueOrPRNumber).
				Str("span_id", meta.SpanID).
				Str("source_type", candidate.source).
				Str("delivery_id", artifact.GitHubDeliveryID).
				Str("artifact_source_type", artifact.SourceType).
				Int64("github_comment_id", artifact.GitHubCommentID).
				Int64("github_review_id", artifact.GitHubReviewID).
				Int64("github_review_comment_id", artifact.GitHubReviewCommentID).
				Str("operation", "upsert_run_attribution_artifact").
				Msg("observe write failed")
		}
	}
}
