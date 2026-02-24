package ai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIssueRefinePromptIncludesRequirements(t *testing.T) {
	store := buildPromptStore(t)
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
	store := buildPromptStore(t)
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
}

func buildPromptStore(t *testing.T) *PromptStore {
	t.Helper()
	dir := t.TempDir()
	issueDir := filepath.Join(dir, "issue_refinement_prompts")
	prDir := filepath.Join(dir, "pr_review_prompts", "security")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatalf("mkdir issue prompts: %v", err)
	}
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		t.Fatalf("mkdir pr prompts: %v", err)
	}
	issuePrompt := `## Issue refinement
all previous issue comments
{{.Repo}} #{{.Number}}`
	if err := os.WriteFile(filepath.Join(issueDir, "PROMPT.md"), []byte(issuePrompt), 0o644); err != nil {
		t.Fatalf("write issue prompt: %v", err)
	}
	prPrompt := `{{.AgentHeading}}
all previous PR comments/reviews
{{.Repo}} #{{.Number}} {{.AgentGuidance}}`
	if err := os.WriteFile(filepath.Join(prDir, "PROMPT.md"), []byte(prPrompt), 0o644); err != nil {
		t.Fatalf("write pr prompt: %v", err)
	}
	store, err := NewPromptStore(dir)
	if err != nil {
		t.Fatalf("build prompt store: %v", err)
	}
	return store
}
