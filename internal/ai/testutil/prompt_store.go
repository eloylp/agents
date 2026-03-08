package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/ai"
)

// BuildPromptStore creates a minimal prompt store rooted at a temp dir with
// generic templates for issue refinement and the provided PR/autonomous agents.
func BuildPromptStore(t *testing.T, prAgents []string, autoAgents []string) *ai.PromptStore {
	t.Helper()
	dir := t.TempDir()
	issueDir := filepath.Join(dir, "issue_refinement_prompts")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatalf("mkdir issue prompts: %v", err)
	}
	issueBody := `## Issue refinement
all previous issue comments
issue {{.Repo}} #{{.Number}}`
	if err := os.WriteFile(filepath.Join(issueDir, "PROMPT.md"), []byte(issueBody), 0o644); err != nil {
		t.Fatalf("write issue prompt: %v", err)
	}
	for _, agent := range prAgents {
		prDir := filepath.Join(dir, "pr_review_prompts", agent)
		if err := os.MkdirAll(prDir, 0o755); err != nil {
			t.Fatalf("mkdir pr prompts: %v", err)
		}
		prBody := `{{.AgentHeading}}
all previous PR comments/reviews
pr {{.Repo}} #{{.Number}} {{.WorkflowPartKey}} {{.AgentGuidance}}`
		if err := os.WriteFile(filepath.Join(prDir, "PROMPT.md"), []byte(prBody), 0o644); err != nil {
			t.Fatalf("write pr prompt: %v", err)
		}
	}
	for _, agent := range autoAgents {
		autoDir := filepath.Join(dir, "autonomous", agent)
		if err := os.MkdirAll(autoDir, 0o755); err != nil {
			t.Fatalf("mkdir auto prompts: %v", err)
		}
		if err := os.WriteFile(filepath.Join(autoDir, "PROMPT.md"), []byte("auto {{.AgentName}} {{.Repo}} {{.Task}} {{.MemoryPath}} {{.Memory}}"), 0o644); err != nil {
			t.Fatalf("write auto prompt: %v", err)
		}
	}
	store, err := ai.NewPromptStore(dir)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
	}
	return store
}
