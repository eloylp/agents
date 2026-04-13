package ai_test

import (
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

func TestRenderAgentPromptComposesSkillsAndPrompt(t *testing.T) {
	t.Parallel()
	skills := map[string]config.SkillDef{
		"architect": {Prompt: "Focus on architecture."},
		"testing":   {Prompt: "Focus on tests."},
	}
	agent := config.AgentDef{
		Name:   "reviewer",
		Skills: []string{"architect", "testing"},
		Prompt: "You review PRs.",
	}

	got, err := ai.RenderAgentPrompt(agent, skills, ai.PromptContext{Repo: "owner/repo", Number: 42})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}

	// Skills must appear in list order, before the agent prompt.
	if !strings.Contains(got, "Focus on architecture.") {
		t.Errorf("missing architect guidance in:\n%s", got)
	}
	archIdx := strings.Index(got, "Focus on architecture.")
	testIdx := strings.Index(got, "Focus on tests.")
	promptIdx := strings.Index(got, "You review PRs.")
	if !(archIdx < testIdx && testIdx < promptIdx) {
		t.Errorf("ordering wrong: arch=%d test=%d prompt=%d", archIdx, testIdx, promptIdx)
	}

	// Runtime context appended at the end.
	if !strings.Contains(got, "Repository: owner/repo") {
		t.Errorf("missing repo context:\n%s", got)
	}
	if !strings.Contains(got, "Issue/PR number: 42") {
		t.Errorf("missing issue number:\n%s", got)
	}
}

func TestRenderAgentPromptWithMemory(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{
		Name:   "autonomous",
		Prompt: "Do your job.",
	}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{
		Repo:       "owner/repo",
		MemoryPath: "/tmp/memory.md",
		Memory:     "Last run: fixed #42",
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if !strings.Contains(got, "Memory file: /tmp/memory.md") {
		t.Errorf("missing memory path:\n%s", got)
	}
	if !strings.Contains(got, "Last run: fixed #42") {
		t.Errorf("missing memory content:\n%s", got)
	}
}

func TestRenderAgentPromptEmptyMemoryFormattedExplicitly(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Go."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{
		Repo:       "owner/repo",
		MemoryPath: "/tmp/mem.md",
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if !strings.Contains(got, "Existing memory: (empty)") {
		t.Errorf("empty memory not signalled explicitly:\n%s", got)
	}
}

func TestRenderAgentPromptUnknownSkillErrors(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{
		Name:   "bad",
		Skills: []string{"missing"},
		Prompt: "hi",
	}
	_, err := ai.RenderAgentPrompt(agent, map[string]config.SkillDef{}, ai.PromptContext{})
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("expected unknown-skill error, got %v", err)
	}
}

func TestRenderAgentPromptOmitsRuntimeSectionWhenEmpty(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Do X."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if strings.Contains(got, "## Runtime context") {
		t.Errorf("runtime section should be omitted when context is empty:\n%s", got)
	}
}

func TestNormalizeTokenSanitizesForFilesystemUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"Architect", "architect"},
		{"  Foo Bar  ", "foo bar"},
		{"../evil", "evil"},
		{"a/b/c", "a_b_c"},
	}
	for _, tt := range tests {
		if got := ai.NormalizeToken(tt.in); got != tt.want {
			t.Errorf("NormalizeToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
