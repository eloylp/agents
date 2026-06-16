package observe_test

import (
	"testing"
	"time"

	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/workflow"
)

// seedSpan records a run attribution snapshot via RecordSpan (same path as
// production) so tests can query it through ResolveRunAttribution.
func seedSpan(t *testing.T, s *observe.Store, spanID, workspace, repo, agentName string, number int, at time.Time) {
	t.Helper()
	s.RecordSpan(workflow.SpanInput{
		SpanID:      spanID,
		WorkspaceID: workspace,
		Agent:       agentName,
		Backend:     "claude",
		Repo:        repo,
		Number:      number,
		EventKind:   "pull_request_review.submitted",
		StartedAt:   at,
		FinishedAt:  at.Add(time.Second),
		Status:      "success",
	})
}

// ─── CaptureArtifact + artifact chain resolution ──────────────────────────

// TestCaptureArtifactStoredAndResolvedByReviewCommentID verifies that a PR
// review comment with valid signed metadata is stored as an artifact and that
// subsequent resolution via ReviewCommentID returns exact attribution.
func TestCaptureArtifactStoredAndResolvedByReviewCommentID(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-rv", "default", "owner/repo", "coder", 55, at)

	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 55,
		SpanID:          "span-rv",
		AgentID:         "agent-1",
		AgentName:       "coder",
	}
	body := "Nice change.\n" + attr.HiddenCommentWithSignature("secret", "prod")

	s.CaptureArtifact(observe.RunAttributionArtifactInput{
		WorkspaceID:           "default",
		RepoOwner:             "owner",
		RepoName:              "repo",
		IssueOrPRNumber:       55,
		SourceType:            "pull_request_review_comment",
		GitHubCommentID:       1001,
		GitHubReviewCommentID: 1001,
		GitHubReviewID:        200,
		SourceURL:             "https://github.com/owner/repo/pull/55#discussion_r1001",
		AuthorLogin:           "coder-bot",
	}, body, "")

	// Resolve via ReviewCommentID (same comment, feedback from human reply).
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 55,
		ReviewCommentID: 1001,
	})
	if got.Confidence != observe.AttributionExact {
		t.Fatalf("Confidence = %q, want %q; diagnostic: %s", got.Confidence, observe.AttributionExact, got.Diagnostic)
	}
	if got.Mode != observe.AttributionModeArtifactComment {
		t.Fatalf("Mode = %q, want %q", got.Mode, observe.AttributionModeArtifactComment)
	}
	if got.Snapshot == nil || got.Snapshot.SpanID != "span-rv" {
		t.Fatalf("Snapshot.SpanID = %q, want %q", func() string {
			if got.Snapshot == nil {
				return "<nil>"
			}
			return got.Snapshot.SpanID
		}(), "span-rv")
	}
}

// TestCaptureArtifactResolvedViaInReplyToID verifies that a human inline reply
// with /agents improve and in_reply_to_id pointing at a signed agent comment
// resolves through the parent artifact.
func TestCaptureArtifactResolvedViaInReplyToID(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-parent", "default", "owner/repo", "pr-reviewer", 77, at)

	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 77,
		SpanID:          "span-parent",
		AgentID:         "agent-rv",
		AgentName:       "pr-reviewer",
	}
	agentComment := "LGTM but see inline.\n" + attr.HiddenCommentWithSignature("secret", "prod")

	// Store the agent's comment as an artifact (comment id=500).
	s.CaptureArtifact(observe.RunAttributionArtifactInput{
		WorkspaceID:           "default",
		RepoOwner:             "owner",
		RepoName:              "repo",
		IssueOrPRNumber:       77,
		SourceType:            "pull_request_review_comment",
		GitHubCommentID:       500,
		GitHubReviewCommentID: 500,
		GitHubReviewID:        300,
		SourceURL:             "https://github.com/owner/repo/pull/77#discussion_r500",
		AuthorLogin:           "pr-reviewer-bot",
	}, agentComment, "")

	// Human inline reply: body has /agents improve, in_reply_to_id=500.
	humanBody := "Agreed! /agents improve – please revise the docstring."
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 77,
		ReviewCommentID: 600, // human's own comment (no artifact)
		InReplyToID:     500, // points at agent comment
		Body:            humanBody,
	})
	if got.Confidence != observe.AttributionExact {
		t.Fatalf("Confidence = %q, want %q; diagnostic: %s", got.Confidence, observe.AttributionExact, got.Diagnostic)
	}
	if got.Mode != observe.AttributionModeArtifactParent {
		t.Fatalf("Mode = %q, want %q", got.Mode, observe.AttributionModeArtifactParent)
	}
	if got.Snapshot == nil || got.Snapshot.SpanID != "span-parent" {
		t.Fatalf("Snapshot.SpanID want span-parent, got %v", got.Snapshot)
	}
}

// TestCaptureArtifactResolvedViaReviewID verifies that a human inline review
// comment resolves through the owning pull_request_review_id when the review
// artifact has valid signed metadata.
func TestCaptureArtifactResolvedViaReviewID(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-review", "default", "owner/repo", "pr-reviewer", 88, at)

	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 88,
		SpanID:          "span-review",
		AgentID:         "agent-rv",
		AgentName:       "pr-reviewer",
	}
	reviewBody := "Review summary.\n" + attr.HiddenCommentWithSignature("secret", "prod")

	// Store the PR review artifact (review id=999, no review_comment_id).
	s.CaptureArtifact(observe.RunAttributionArtifactInput{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 88,
		SourceType:      "pull_request_review",
		GitHubReviewID:  999,
		SourceURL:       "https://github.com/owner/repo/pull/88#pullrequestreview-999",
		AuthorLogin:     "pr-reviewer-bot",
	}, reviewBody, "")

	// Human inline comment: pull_request_review_id=999 (points at the review).
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:         "default",
		RepoOwner:           "owner",
		RepoName:            "repo",
		IssueOrPRNumber:     88,
		ReviewCommentID:     700, // human's own comment (no artifact)
		PullRequestReviewID: 999, // points at agent review
		Body:                "/agents improve this explanation",
	})
	if got.Confidence != observe.AttributionExact {
		t.Fatalf("Confidence = %q, want %q; diagnostic: %s", got.Confidence, observe.AttributionExact, got.Diagnostic)
	}
	if got.Mode != observe.AttributionModeArtifactReview {
		t.Fatalf("Mode = %q, want %q", got.Mode, observe.AttributionModeArtifactReview)
	}
	if got.Snapshot == nil || got.Snapshot.SpanID != "span-review" {
		t.Fatalf("Snapshot.SpanID want span-review, got %v", got.Snapshot)
	}
}

// TestCaptureArtifactRejectsWrongRepoMetadata verifies that signed metadata
// from a different repo is rejected and not stored as an authoritative artifact.
func TestCaptureArtifactRejectsWrongRepoMetadata(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-other", "default", "other/repo", "coder", 10, at)

	// Metadata from other/repo, but we're in owner/repo context.
	attrOther := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "other",
		RepoName:        "repo",
		IssueOrPRNumber: 10,
		SpanID:          "span-other",
		AgentName:       "coder",
	}
	body := "comment " + attrOther.HiddenCommentWithSignature("secret", "prod")

	// CaptureArtifact in owner/repo context should reject.
	s.CaptureArtifact(observe.RunAttributionArtifactInput{
		WorkspaceID:           "default",
		RepoOwner:             "owner",
		RepoName:              "repo",
		IssueOrPRNumber:       10,
		SourceType:            "pull_request_review_comment",
		GitHubReviewCommentID: 111,
	}, body, "")

	// No artifact should be stored; ReviewCommentID lookup returns unresolved.
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 10,
		ReviewCommentID: 111,
	})
	if got.Confidence == observe.AttributionExact {
		t.Fatalf("expected non-exact confidence for wrong-repo metadata, got exact with span %q", func() string {
			if got.Snapshot != nil {
				return got.Snapshot.SpanID
			}
			return "<nil>"
		}())
	}
}

// TestCaptureArtifactRejectsInvalidSignature verifies that invalid/tampered
// signatures are logged and ignored; the artifact is not stored.
func TestCaptureArtifactRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-sig", "default", "owner/repo", "coder", 20, at)

	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 20,
		SpanID:          "span-sig",
		AgentName:       "coder",
	}
	validComment := attr.HiddenCommentWithSignature("secret", "prod")
	// Tamper the signature.
	tampered := validComment[:len(validComment)-5] + "XXXXX"

	s.CaptureArtifact(observe.RunAttributionArtifactInput{
		WorkspaceID:           "default",
		RepoOwner:             "owner",
		RepoName:              "repo",
		IssueOrPRNumber:       20,
		SourceType:            "pull_request_review_comment",
		GitHubReviewCommentID: 222,
	}, tampered, "")

	// Artifact must not be stored.
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 20,
		ReviewCommentID: 222,
	})
	if got.Confidence == observe.AttributionExact {
		t.Fatalf("expected non-exact confidence after tampered signature, got exact")
	}
}

// TestArtifactAmbiguousPRContextReturnUnresolved verifies that multiple PR
// artifact candidates for the same context produce an ambiguous/unresolved result.
func TestArtifactAmbiguousPRContextReturnUnresolved(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-a1", "default", "owner/repo", "coder", 30, at)
	seedSpan(t, s, "span-a2", "default", "owner/repo", "pr-reviewer", 30, at)

	for _, tc := range []struct {
		span  string
		rcID  int64
		agent string
	}{
		{"span-a1", 801, "coder"},
		{"span-a2", 802, "pr-reviewer"},
	} {
		attr := workflow.RunAttribution{
			WorkspaceID:     "default",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueOrPRNumber: 30,
			SpanID:          tc.span,
			AgentName:       tc.agent,
		}
		s.CaptureArtifact(observe.RunAttributionArtifactInput{
			WorkspaceID:           "default",
			RepoOwner:             "owner",
			RepoName:              "repo",
			IssueOrPRNumber:       30,
			SourceType:            "pull_request_review_comment",
			GitHubReviewCommentID: tc.rcID,
			FilePath:              "pkg/foo.go",
			CommitSHA:             "abc123",
		}, "code comment "+attr.HiddenCommentWithSignature("secret", "prod"), "")
	}

	// Query by PR context only (no specific comment/review id) should find
	// multiple candidates and return unresolved/ambiguous.
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 30,
		FilePath:        "pkg/foo.go",
		HeadSHA:         "abc123",
	})
	if got.Confidence == observe.AttributionExact {
		t.Fatalf("expected ambiguous/unresolved for multiple PR context candidates, got exact (span=%q)", func() string {
			if got.Snapshot != nil {
				return got.Snapshot.SpanID
			}
			return "<nil>"
		}())
	}
}

// TestInferenceSkipsInternalAnalystRuns verifies that time-window inference
// never attributes feedback to internal-catalog-analyst runs. Only exact
// signed metadata or artifact ancestry can reach those agents.
func TestInferenceSkipsInternalAnalystRuns(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	// No signing secret → legacy mode, but still skip internal analysts.
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

	// Seed one internal analyst span and one regular coder span on the same PR.
	seedSpan(t, s, "span-analyst", "default", "owner/repo", "internal-catalog-analyst", 40, at)
	seedSpan(t, s, "span-coder", "default", "owner/repo", "coder", 40, at)

	// Query by time window; should return only the coder span.
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 40,
		At:              at.Add(30 * time.Second),
		Window:          5 * time.Minute,
	})
	if got.Confidence != observe.AttributionInferred {
		t.Fatalf("Confidence = %q, want inferred; diagnostic: %s", got.Confidence, got.Diagnostic)
	}
	if got.Snapshot == nil || got.Snapshot.SpanID != "span-coder" {
		t.Fatalf("expected span-coder to be inferred, got %v", got.Snapshot)
	}

	// If only the internal analyst is present, inference returns unresolved.
	s2 := testDB(t)
	seedSpan(t, s2, "span-analyst-only", "default", "owner/repo", "internal-catalog-analyst", 50, at)
	got2 := s2.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 50,
		At:              at.Add(30 * time.Second),
		Window:          5 * time.Minute,
	})
	if got2.Confidence == observe.AttributionInferred {
		t.Fatalf("inference should not attribute to internal analyst, got inferred with span %q", func() string {
			if got2.Snapshot != nil {
				return got2.Snapshot.SpanID
			}
			return "<nil>"
		}())
	}
	if got2.Confidence != observe.AttributionUnresolved {
		t.Fatalf("expected unresolved when only internal analyst exists, got %q", got2.Confidence)
	}
}

// TestFeedbackStoredWithUnresolvedWhenNoOwnership verifies that attribution
// remains unresolved (not panicking or corrupting) when no valid artifact
// ownership exists. The resolution path should still produce a valid result.
func TestFeedbackStoredWithUnresolvedWhenNoOwnership(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})

	// No spans seeded, no artifacts. Query should return unresolved gracefully.
	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:         "default",
		RepoOwner:           "owner",
		RepoName:            "repo",
		IssueOrPRNumber:     99,
		ReviewCommentID:     999,
		InReplyToID:         888,
		PullRequestReviewID: 777,
		Body:                "/agents improve this",
	})
	if got.Confidence != observe.AttributionUnresolved {
		t.Fatalf("Confidence = %q, want unresolved", got.Confidence)
	}
}

// TestDirectSignedMetadataStillResolvesExact verifies that the existing direct
// signed metadata path continues to work alongside artifact resolution.
func TestDirectSignedMetadataStillResolvesExact(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	s.WithAttributionVerifier(observe.AttributionVerifierConfig{
		SigningSecret: "secret",
		InstanceID:    "prod",
	})
	at := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedSpan(t, s, "span-direct", "default", "owner/repo", "coder", 11, at)

	attr := workflow.RunAttribution{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 11,
		SpanID:          "span-direct",
		AgentName:       "coder",
	}
	body := "/agents improve this\n" + attr.HiddenCommentWithSignature("secret", "prod")

	got := s.ResolveRunAttribution(observe.AttributionQuery{
		WorkspaceID:     "default",
		RepoOwner:       "owner",
		RepoName:        "repo",
		IssueOrPRNumber: 11,
		Body:            body,
	})
	if got.Confidence != observe.AttributionExact {
		t.Fatalf("Confidence = %q, want exact; diagnostic: %s", got.Confidence, got.Diagnostic)
	}
	if got.Mode != observe.AttributionModeDirect {
		t.Fatalf("Mode = %q, want %q", got.Mode, observe.AttributionModeDirect)
	}
	if got.Snapshot == nil || got.Snapshot.SpanID != "span-direct" {
		t.Fatalf("Snapshot.SpanID want span-direct, got %v", got.Snapshot)
	}
}
