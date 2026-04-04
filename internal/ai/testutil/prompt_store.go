package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/ai"
)

// BuildPromptStore creates a minimal prompt store rooted at a temp dir with
// generic templates for issue refinement and the provided PR/autonomous agents.
// Each agent gets a single skill with the same name.
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
	prBaseDir := filepath.Join(dir, "pr_review_prompts", "base")
	if err := os.MkdirAll(prBaseDir, 0o755); err != nil {
		t.Fatalf("mkdir pr base: %v", err)
	}
	prBaseBody := `{{.AgentHeading}}
all previous PR comments/reviews
{{template "agent_guidance" .}}
pr {{.Repo}} #{{.Number}} {{.WorkflowPartKey}}`
	if err := os.WriteFile(filepath.Join(prBaseDir, "PROMPT.md"), []byte(prBaseBody), 0o644); err != nil {
		t.Fatalf("write pr base: %v", err)
	}
	guidanceDir := filepath.Join(dir, "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		t.Fatalf("mkdir guidance: %v", err)
	}

	// Collect all unique skill names from both agent lists.
	allNames := make(map[string]struct{})
	for _, name := range prAgents {
		allNames[name] = struct{}{}
	}
	for _, name := range autoAgents {
		allNames[name] = struct{}{}
	}

	// Build skills with file-based guidance (raw text, no {{define}} wrapper).
	var skills []ai.SkillGuidance
	for name := range allNames {
		filePath := filepath.Join(guidanceDir, name+".md")
		writeSkillGuidance(t, guidanceDir, name+".md", "pr guidance "+name)
		skills = append(skills, ai.SkillGuidance{Name: name, PromptFile: filePath})
	}

	// Build agent-to-skill mappings: each agent maps to a single same-named skill.
	prAS := make([]ai.AgentSkills, len(prAgents))
	for i, name := range prAgents {
		prAS[i] = ai.AgentSkills{Name: name, Skills: []string{name}}
	}
	autoAS := make([]ai.AgentSkills, len(autoAgents))
	for i, name := range autoAgents {
		autoAS[i] = ai.AgentSkills{Name: name, Skills: []string{name}}
	}

	autoBaseDir := filepath.Join(dir, "autonomous", "base")
	if err := os.MkdirAll(autoBaseDir, 0o755); err != nil {
		t.Fatalf("mkdir auto base: %v", err)
	}
	autoBaseBody := `auto {{.AgentName}} {{.Repo}} {{.Task}} {{.MemoryPath}}
{{template "agent_guidance" .}}`
	if err := os.WriteFile(filepath.Join(autoBaseDir, "PROMPT.md"), []byte(autoBaseBody), 0o644); err != nil {
		t.Fatalf("write auto base: %v", err)
	}

	issueBase := ai.PromptSource{PromptFile: filepath.Join(issueDir, "PROMPT.md")}
	prBase := ai.PromptSource{PromptFile: filepath.Join(prBaseDir, "PROMPT.md")}
	autoBase := ai.PromptSource{PromptFile: filepath.Join(autoBaseDir, "PROMPT.md")}

	store, err := ai.NewPromptStore(issueBase, prBase, autoBase, skills, prAS, autoAS)
	if err != nil {
		t.Fatalf("prompt store: %v", err)
	}
	return store
}

func writeSkillGuidance(t *testing.T, dir string, filename string, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir guidance: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write guidance: %v", err)
	}
}
