package ai_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/ai"
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
		t.Fatalf("expected skill-specific guidance in prompt")
	}
}

func TestBuildPRReviewPromptWithInlineSkill(t *testing.T) {
	issueBase, prBase, autoBase := makeTestPromptSources(t)
	skills := []ai.SkillGuidance{
		{Name: "custom", Prompt: "Focus on custom things like widgets and gadgets."},
	}
	prAgents := []ai.AgentSkills{
		{Name: "custom", Skills: []string{"custom"}},
	}
	store, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, nil)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
	}
	prompt, err := store.PRReviewPrompt("custom", "claude", "owner/repo", 1)
	if err != nil {
		t.Fatalf("pr prompt error: %v", err)
	}
	if !strings.Contains(prompt, "widgets and gadgets") {
		t.Fatalf("expected inline guidance in prompt, got: %s", prompt)
	}
}

func TestBuildPRReviewPromptWithMultipleSkills(t *testing.T) {
	issueBase, prBase, autoBase := makeTestPromptSources(t)
	skills := []ai.SkillGuidance{
		{Name: "architect", Prompt: "Focus on architecture."},
		{Name: "security", Prompt: "Focus on security."},
	}
	prAgents := []ai.AgentSkills{
		{Name: "full-reviewer", Skills: []string{"architect", "security"}},
	}
	store, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, nil)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
	}
	prompt, err := store.PRReviewPrompt("full-reviewer", "claude", "owner/repo", 1)
	if err != nil {
		t.Fatalf("pr prompt error: %v", err)
	}
	if !strings.Contains(prompt, "Focus on architecture.") {
		t.Fatalf("expected architect guidance in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Focus on security.") {
		t.Fatalf("expected security guidance in prompt, got: %s", prompt)
	}
}

// makeTestPromptSources creates minimal on-disk fixture files for issue and PR
// base prompts, returning the three PromptSource values used by most PR prompt
// tests. Errors are fatal so callers don't need to check them.
func makeTestPromptSources(t *testing.T) (issueBase, prBase, autoBase ai.PromptSource) {
	t.Helper()
	dir := t.TempDir()
	issueDir := filepath.Join(dir, "issue_refinement_prompts")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatalf("mkdir issue prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(issueDir, "PROMPT.md"), []byte("issue {{.Repo}} #{{.Number}}"), 0o644); err != nil {
		t.Fatalf("write issue prompt: %v", err)
	}
	prBaseDir := filepath.Join(dir, "pr_review_prompts", "base")
	if err := os.MkdirAll(prBaseDir, 0o755); err != nil {
		t.Fatalf("mkdir pr base: %v", err)
	}
	prBasePath := filepath.Join(prBaseDir, "PROMPT.md")
	if err := os.WriteFile(prBasePath, []byte("{{.AgentHeading}}\n{{template \"agent_guidance\" .}}"), 0o644); err != nil {
		t.Fatalf("write pr base prompt: %v", err)
	}
	return ai.PromptSource{PromptFile: filepath.Join(issueDir, "PROMPT.md")},
		ai.PromptSource{PromptFile: prBasePath},
		ai.PromptSource{Prompt: `{{.Task}} {{template "agent_guidance" .}}`}
}

func TestPromptStoreValidateFailsOnMissingTemplate(t *testing.T) {
	skills := []ai.SkillGuidance{
		{Name: "security", PromptFile: "/nonexistent/security.md"},
	}
	prAgents := []ai.AgentSkills{
		{Name: "security", Skills: []string{"security"}},
	}
	issueBase := ai.PromptSource{PromptFile: "/nonexistent/issue.md"}
	prBase := ai.PromptSource{PromptFile: "/nonexistent/pr.md"}
	autoBase := ai.PromptSource{PromptFile: "/nonexistent/auto.md"}
	if _, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, nil); err == nil {
		t.Fatalf("expected construction failure for missing templates")
	}
}

func TestPromptStoreRejectsIssueTemplateWithUnknownField(t *testing.T) {
	t.Parallel()
	// {{.Nubmer}} is a typo for {{.Number}}; the error must surface at startup.
	issueBase := ai.PromptSource{Prompt: "issue {{.Repo}} #{{.Nubmer}}"}
	prBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	autoBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	_, err := ai.NewPromptStore(issueBase, prBase, autoBase, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown field .Nubmer in issue template, got nil")
	}
	if !strings.Contains(err.Error(), "Nubmer") {
		t.Fatalf("expected error to mention the unknown field, got: %v", err)
	}
}

func TestPromptStoreRejectsPRTemplateWithUnknownField(t *testing.T) {
	t.Parallel()
	// {{.AgentHeadng}} is a typo for {{.AgentHeading}}.
	issueBase := ai.PromptSource{Prompt: "issue {{.Repo}} #{{.Number}}"}
	prBase := ai.PromptSource{Prompt: `{{.AgentHeadng}} {{template "agent_guidance" .}}`}
	autoBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	skills := []ai.SkillGuidance{{Name: "sec", Prompt: "security guidance"}}
	prAgents := []ai.AgentSkills{{Name: "sec", Skills: []string{"sec"}}}
	_, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAgents, nil)
	if err == nil {
		t.Fatal("expected error for unknown field .AgentHeadng in pr template, got nil")
	}
	if !strings.Contains(err.Error(), "AgentHeadng") {
		t.Fatalf("expected error to mention the unknown field, got: %v", err)
	}
}

func TestPromptStoreRejectsAutonomousTemplateWithUnknownField(t *testing.T) {
	t.Parallel()
	// {{.AgentNam}} is a typo for {{.AgentName}}.
	issueBase := ai.PromptSource{Prompt: "issue {{.Repo}} #{{.Number}}"}
	prBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	autoBase := ai.PromptSource{Prompt: `{{.AgentNam}} {{template "agent_guidance" .}}`}
	skills := []ai.SkillGuidance{{Name: "sec", Prompt: "security guidance"}}
	autoAgents := []ai.AgentSkills{{Name: "sec", Skills: []string{"sec"}}}
	_, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, nil, autoAgents)
	if err == nil {
		t.Fatal("expected error for unknown field .AgentNam in autonomous template, got nil")
	}
	if !strings.Contains(err.Error(), "AgentNam") {
		t.Fatalf("expected error to mention the unknown field, got: %v", err)
	}
}

func TestPromptStoreErrorIncludesSourceDescription(t *testing.T) {
	t.Parallel()
	// Confirm the error message includes a useful source reference.
	issueBase := ai.PromptSource{Prompt: "issue {{.Nubmer}}"}
	prBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	autoBase := ai.PromptSource{Prompt: `{{template "agent_guidance" .}}`}
	_, err := ai.NewPromptStore(issueBase, prBase, autoBase, nil, nil, nil)
	if err == nil {
		t.Fatal("expected construction error, got nil")
	}
	if !strings.Contains(err.Error(), "inline prompt") {
		t.Fatalf("expected error to reference 'inline prompt', got: %v", err)
	}
}
