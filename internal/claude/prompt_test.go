package claude

import (
	"strings"
	"testing"
)

func TestBuildIssueRefinePromptIncludesMarker(t *testing.T) {
	prompt := BuildIssueRefinePrompt("owner/repo", 12, "fingerprint", "ai:refine")
	if !strings.Contains(prompt, "ai-daemon:issue-refine") {
		t.Fatalf("expected issue refine marker in prompt")
	}
	if !strings.Contains(prompt, "fingerprint=fingerprint") {
		t.Fatalf("expected fingerprint marker in prompt")
	}
}

func TestBuildPRReviewPromptIncludesMarker(t *testing.T) {
	prompt := BuildPRReviewPrompt("owner/repo", 4, "fingerprint", "ai:review")
	if !strings.Contains(prompt, "ai-daemon:pr-review") {
		t.Fatalf("expected pr review marker in prompt")
	}
	if !strings.Contains(prompt, "fingerprint=fingerprint") {
		t.Fatalf("expected fingerprint marker in prompt")
	}
}
