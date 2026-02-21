package ai

import (
	"strings"
	"testing"
)

func TestBuildIssueRefinePromptIncludesRequirements(t *testing.T) {
	prompt := BuildIssueRefinePrompt("owner/repo", 12)
	if !strings.Contains(prompt, "## Issue refinement") {
		t.Fatalf("expected issue refinement heading in prompt")
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
