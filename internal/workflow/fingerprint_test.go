package workflow

import (
	"testing"
	"time"
)

func TestIssueFingerprintStable(t *testing.T) {
	issue := Issue{
		Number:    42,
		Title:     "Add daemon",
		Body:      "Need a poller",
		UpdatedAt: time.Date(2026, 2, 12, 9, 0, 0, 0, time.UTC),
	}
	first := IssueFingerprint(issue, 2000)
	second := IssueFingerprint(issue, 2000)
	if first != second {
		t.Fatalf("expected stable fingerprint, got %s vs %s", first, second)
	}
}

func TestPRFingerprintChangesWithHead(t *testing.T) {
	pr := PullRequest{
		Number: 7,
	}
	pr.Head.SHA = "abc"
	pr.Title = "Change"
	pr.Body = "Body"
	first := PRFingerprint(pr, "security", 2000)
	pr.Head.SHA = "def"
	second := PRFingerprint(pr, "security", 2000)
	if first == second {
		t.Fatalf("expected fingerprint to change when head SHA changes")
	}
}

func TestPRFingerprintChangesWithRole(t *testing.T) {
	pr := PullRequest{Number: 9}
	pr.Head.SHA = "abc"
	security := PRFingerprint(pr, "security", 2000)
	testingRole := PRFingerprint(pr, "testing", 2000)
	if security == testingRole {
		t.Fatalf("expected fingerprint to change when role changes")
	}
}
