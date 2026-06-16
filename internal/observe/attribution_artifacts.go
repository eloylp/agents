package observe

import (
	"encoding/json"
	"log"

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
		if err := s.store.UpsertRunAttributionArtifact(artifact); err != nil {
			log.Printf("observe: artifact capture: upsert %s span=%s: %v", candidate.source, meta.SpanID, err)
		}
	}
}
