package claude

import (
	"strings"
	"testing"
)

func TestBuildIssueRefinePromptIncludesMarker(t *testing.T) {
	prompt := BuildIssueRefinePrompt("claude", "owner/repo", 12, "fingerprint")
	if !strings.Contains(prompt, "ai-daemon:issue-refine") {
		t.Fatalf("expected issue refine marker in prompt")
	}
	if !strings.Contains(prompt, "fingerprint=fingerprint") {
		t.Fatalf("expected fingerprint marker in prompt")
	}
	if !strings.Contains(prompt, "## claude refinement") {
		t.Fatalf("expected agent heading in prompt")
	}
}

func TestBuildPRReviewPromptIncludesMarker(t *testing.T) {
	prompt := BuildPRReviewPrompt("claude", "security", "owner/repo", 4, "fingerprint")
	if !strings.Contains(prompt, "ai-daemon:pr-review") {
		t.Fatalf("expected pr review marker in prompt")
	}
	if !strings.Contains(prompt, "fingerprint=fingerprint") {
		t.Fatalf("expected fingerprint marker in prompt")
	}
	if !strings.Contains(prompt, "## claude specialist: security") {
		t.Fatalf("expected specialist heading in prompt")
	}
}
