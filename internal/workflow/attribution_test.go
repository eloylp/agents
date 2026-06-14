package workflow_test

import (
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/workflow"
)

func TestRunAttributionSignedMetadataVerifies(t *testing.T) {
	t.Parallel()
	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "Owner",
		RepoName:        "Repo",
		IssueOrPRNumber: 42,
		SpanID:          "span-1",
		AgentID:         "agent-1",
		AgentName:       "coder",
	}

	meta := attr.PublicMetadata("secret", "prod-a")
	if meta.Signature == "" {
		t.Fatal("Signature is empty")
	}
	if meta.InstanceID != "prod-a" {
		t.Fatalf("InstanceID = %q, want prod-a", meta.InstanceID)
	}
	if err := workflow.VerifyPublicRunAttribution(meta, "secret", "prod-a"); err != nil {
		t.Fatalf("VerifyPublicRunAttribution: %v", err)
	}
	if err := workflow.VerifyPublicRunAttribution(meta, "wrong-secret", "prod-a"); err == nil {
		t.Fatal("VerifyPublicRunAttribution with wrong secret got nil error, want failure")
	}
	if err := workflow.VerifyPublicRunAttribution(meta, "secret", "prod-b"); err == nil {
		t.Fatal("VerifyPublicRunAttribution with wrong instance got nil error, want failure")
	}
}

func TestRunAttributionCommitTrailerRoundTrip(t *testing.T) {
	t.Parallel()
	attr := workflow.RunAttribution{
		WorkspaceID: "default",
		RepoOwner:   "owner",
		RepoName:    "repo",
		SpanID:      "span-1",
		AgentName:   "coder",
	}
	trailer := attr.CommitAttributionTrailer("secret", "prod-a")
	value, ok := strings.CutPrefix(trailer, "Agents-Attribution: ")
	if !ok {
		t.Fatalf("CommitAttributionTrailer = %q, want Agents-Attribution prefix", trailer)
	}
	meta, err := workflow.DecodeCommitAttributionTrailer(value)
	if err != nil {
		t.Fatalf("DecodeCommitAttributionTrailer: %v", err)
	}
	if err := workflow.VerifyPublicRunAttribution(meta, "secret", "prod-a"); err != nil {
		t.Fatalf("VerifyPublicRunAttribution: %v", err)
	}
}
