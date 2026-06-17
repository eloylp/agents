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
