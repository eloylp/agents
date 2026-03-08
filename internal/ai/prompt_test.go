package ai_test

import (
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/ai/testutil"
)

func TestBuildIssueRefinePromptIncludesRequirements(t *testing.T) {
	store := testutil.BuildPromptStore(t, []string{"security"}, nil)
	prompt, err := store.IssueRefinePrompt("owner/repo", 12)
	if err != nil {
		t.Fatalf("issue prompt error: %v", err)
	}
	if !strings.Contains(prompt, "## Issue refinement") {
		t.Fatalf("expected issue refinement heading in prompt")
	}
	if !strings.Contains(prompt, "all previous issue comments") {
		t.Fatalf("expected full issue comment reading requirement in prompt")
	}
}

func TestBuildPRReviewPromptIncludesRequirements(t *testing.T) {
	store := testutil.BuildPromptStore(t, []string{"security"}, nil)
	prompt, err := store.PRReviewPrompt("security", "claude", "owner/repo", 4)
	if err != nil {
		t.Fatalf("pr prompt error: %v", err)
	}
	if !strings.Contains(prompt, "## claude specialist: security") {
		t.Fatalf("expected specialist heading in prompt")
	}
	if !strings.Contains(prompt, "all previous PR comments/reviews") {
		t.Fatalf("expected full pr comments/reviews reading requirement in prompt")
	}
	if !strings.Contains(prompt, "pr guidance security") {
		t.Fatalf("expected agent-specific guidance in prompt")
	}
}

func TestPromptStoreValidateFailsOnMissingTemplate(t *testing.T) {
	store := testutil.BuildPromptStore(t, nil, nil)
	if err := store.Validate([]string{"security"}, []string{"architect"}); err == nil {
		t.Fatalf("expected validation failure for missing templates")
	}
}
