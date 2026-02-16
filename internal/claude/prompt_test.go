package claude

import (
	"strings"
	"testing"
)

func TestBuildIssueRefinePromptIncludesRequirements(t *testing.T) {
	prompt := BuildIssueRefinePrompt("claude", "owner/repo", 12)
	if !strings.Contains(prompt, "## claude refinement") {
		t.Fatalf("expected agent heading in prompt")
	}
	if !strings.Contains(prompt, "all previous issue comments") {
		t.Fatalf("expected full issue comment reading requirement in prompt")
	}
}

func TestBuildPRReviewPromptIncludesRequirements(t *testing.T) {
	prompt := BuildPRReviewPrompt("claude", "security", "owner/repo", 4)
	if !strings.Contains(prompt, "## claude specialist: security") {
		t.Fatalf("expected specialist heading in prompt")
	}
	if !strings.Contains(prompt, "all previous PR comments/reviews") {
		t.Fatalf("expected full pr comments/reviews reading requirement in prompt")
	}
}
