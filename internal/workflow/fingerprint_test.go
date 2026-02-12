package workflow

import (
	"testing"
	"time"

	"github.com/eloylp/agents/internal/github"
)

func TestIssueFingerprintStable(t *testing.T) {
	issue := github.Issue{
		Number:    42,
		Title:     "Add daemon",
		Body:      "Need a poller",
		UpdatedAt: time.Date(2026, 2, 12, 9, 0, 0, 0, time.UTC),
	}
	comments := []github.Comment{{Body: "Looks good"}}
	first := IssueFingerprint(issue, comments, 2000)
	second := IssueFingerprint(issue, comments, 2000)
	if first != second {
		t.Fatalf("expected stable fingerprint, got %s vs %s", first, second)
	}
}

func TestPRFingerprintChangesWithHead(t *testing.T) {
	pr := github.PullRequest{
		Number: 7,
	}
	pr.Head.SHA = "abc"
	files := []github.PullFile{{Filename: "main.go", Status: "modified", Additions: 1, Deletions: 0, Patch: "diff"}}
	first := PRFingerprint(pr, files, 2000)
	pr.Head.SHA = "def"
	second := PRFingerprint(pr, files, 2000)
	if first == second {
		t.Fatalf("expected fingerprint to change when head SHA changes")
	}
}
