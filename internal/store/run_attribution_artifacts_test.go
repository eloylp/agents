package store_test

import (
	"testing"
	"time"

	"github.com/eloylp/agents/internal/store"
)

func TestRunAttributionArtifactUpsertAndLookups(t *testing.T) {
	t.Parallel()
	st := store.New(openTestDB(t))
	t.Cleanup(func() { st.Close() })

	created := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	inputs := []store.RunAttributionArtifactInput{
		{
			WorkspaceID:           " default ",
			RepoOwner:             " owner ",
			RepoName:              " repo ",
			IssueOrPRNumber:       42,
			SourceType:            " pull_request_review_comment ",
			GitHubCommentID:       1001,
			GitHubReviewID:        2001,
			GitHubReviewCommentID: 1001,
			GitHubParentCommentID: 900,
			GitHubDeliveryID:      " delivery-1 ",
			SourceURL:             " https://github.com/owner/repo/pull/42#discussion_r1001 ",
			AuthorLogin:           " coder ",
			FilePath:              " internal/foo.go ",
			Line:                  12,
			Side:                  " RIGHT ",
			CommitSHA:             " abc123 ",
			SpanID:                " span-comment ",
			MetadataJSON:          `{"span_id":"span-comment"}`,
			GitHubCreatedAt:       &created,
			GitHubUpdatedAt:       &updated,
		},
		{
			WorkspaceID:     "default",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueOrPRNumber: 42,
			SourceType:      "pull_request_review",
			GitHubReviewID:  2002,
			SourceURL:       "https://github.com/owner/repo/pull/42#pullrequestreview-2002",
			AuthorLogin:     "pr-reviewer",
			FilePath:        "internal/foo.go",
			CommitSHA:       "abc123",
			SpanID:          "span-review",
			MetadataJSON:    `{"span_id":"span-review"}`,
		},
		{
			WorkspaceID:     "default",
			RepoOwner:       "owner",
			RepoName:        "repo",
			IssueOrPRNumber: 43,
			SourceType:      "issue_comment",
			GitHubCommentID: 3001,
			SpanID:          "span-issue",
		},
		{
			WorkspaceID: "default",
			RepoOwner:   "owner",
			RepoName:    "repo",
			SourceType:  "commit",
			CommitSHA:   "def456",
			SpanID:      "span-commit",
		},
	}
	for _, in := range inputs {
		if err := st.UpsertRunAttributionArtifact(in); err != nil {
			t.Fatalf("upsert artifact %+v: %v", in, err)
		}
	}
	if err := st.UpsertRunAttributionArtifact(inputs[0]); err != nil {
		t.Fatalf("idempotent upsert: %v", err)
	}

	tests := []struct {
		name   string
		lookup func() (store.RunAttributionArtifact, bool)
		want   string
	}{
		{
			name: "review comment id",
			lookup: func() (store.RunAttributionArtifact, bool) {
				return st.RunAttributionArtifactByReviewCommentID("default", "owner", "repo", 1001)
			},
			want: "span-comment",
		},
		{
			name: "review id excludes review comments",
			lookup: func() (store.RunAttributionArtifact, bool) {
				return st.RunAttributionArtifactByReviewID("default", "owner", "repo", 2002)
			},
			want: "span-review",
		},
		{
			name: "issue comment id",
			lookup: func() (store.RunAttributionArtifact, bool) {
				return st.RunAttributionArtifactByCommentID("default", "owner", "repo", "issue_comment", 3001)
			},
			want: "span-issue",
		},
		{
			name: "commit sha",
			lookup: func() (store.RunAttributionArtifact, bool) {
				return st.RunAttributionArtifactByCommitSHA("default", "owner", "repo", "def456")
			},
			want: "span-commit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.lookup()
			if !ok {
				t.Fatalf("lookup ok = false, want true")
			}
			if got.SpanID != tt.want {
				t.Fatalf("SpanID = %q, want %q", got.SpanID, tt.want)
			}
		})
	}

	matches := st.RunAttributionArtifactsByPRContext("default", "owner", "repo", 42, "internal/foo.go", "abc123")
	if len(matches) != 2 {
		t.Fatalf("PR context matches = %d, want 2", len(matches))
	}
	if _, ok := st.RunAttributionArtifactByReviewID("default", "owner", "repo", 2001); ok {
		t.Fatalf("review id lookup matched a review comment row, want false")
	}
}

func TestRunAttributionArtifactLookupParsesLiveTimestampText(t *testing.T) {
	t.Parallel()
	st := store.New(openTestDB(t))
	t.Cleanup(func() { st.Close() })

	_, err := st.DB().Exec(`
		INSERT INTO run_attribution_artifacts (
			workspace_id, repo_owner, repo_name, issue_or_pr_number,
			source_type, commit_sha, span_id, metadata_json,
			github_created_at, github_updated_at, observed_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"self-improvement-demo", "eloylp", "test-acme-repo", 0,
		"commit", "82170ee3de01de12873a0e4dad3ebbc96759166f", "50cc824e1d7faf8d", `{"span_id":"50cc824e1d7faf8d"}`,
		"2026-06-18 13:06:18 +0200 +0200", "2026-06-18 13:06:18 +0200 +0200", "2026-06-18 11:06:20",
	)
	if err != nil {
		t.Fatalf("insert live-shaped artifact: %v", err)
	}

	got, ok := st.RunAttributionArtifactByCommitSHA("self-improvement-demo", "eloylp", "test-acme-repo", "82170ee3de01de12873a0e4dad3ebbc96759166f")
	if !ok {
		t.Fatalf("lookup ok = false, want true")
	}
	if got.SpanID != "50cc824e1d7faf8d" {
		t.Fatalf("SpanID = %q, want 50cc824e1d7faf8d", got.SpanID)
	}
	if got.GitHubCreatedAt == nil {
		t.Fatalf("GitHubCreatedAt = nil, want parsed time")
	}
	if got.ObservedAt.IsZero() {
		t.Fatalf("ObservedAt is zero, want parsed time")
	}
}
