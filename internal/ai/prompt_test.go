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

func TestRenderAgentPromptIncludesEventContext(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to comments."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{
		Repo:      "owner/repo",
		Number:    3,
		EventKind: "issue_comment.created",
		Actor:     "octocat",
		Payload:   map[string]any{"body": "LGTM"},
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if !strings.Contains(got, "Event: issue_comment.created") {
		t.Errorf("missing event kind:\n%s", got)
	}
	if !strings.Contains(got, "Actor: octocat") {
		t.Errorf("missing actor:\n%s", got)
	}
	if !strings.Contains(got, "body: LGTM") {
		t.Errorf("missing payload body:\n%s", got)
	}
}

func TestRenderAgentPromptMultilinePayloadBodyIsIndented(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to comments."}
	body := "first line\nsecond line\nthird line"
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{
		Repo:      "owner/repo",
		Number:    7,
		EventKind: "issue_comment.created",
		Actor:     "octocat",
		Payload:   map[string]any{"body": body},
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	// Multiline values must be rendered as indented block, not bare continuation lines.
	if strings.Contains(got, "body: first line") {
		t.Errorf("multiline body rendered inline (not indented):\n%s", got)
	}
	if !strings.Contains(got, "body:\n  first line\n  second line\n  third line\n") {
		t.Errorf("multiline body not rendered as indented block:\n%s", got)
	}
}

func TestRenderAgentPromptRosterRenderedAlphabetically(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Do work."}
	ctx := ai.PromptContext{
		Repo: "owner/repo",
		Roster: []ai.RosterEntry{
			{Name: "pr-reviewer", Description: "Reviews PRs", Skills: []string{"testing"}, AllowDispatch: true},
			{Name: "arch-reviewer", Description: "Reviews arch", Skills: []string{"architect"}, AllowDispatch: false},
			{Name: "sec-reviewer", Description: "Reviews security", Skills: []string{"security"}, AllowDispatch: true},
		},
	}
	got, err := ai.RenderAgentPrompt(agent, nil, ctx)
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if !strings.Contains(got, "## Available experts") {
		t.Errorf("missing experts section:\n%s", got)
	}
	archIdx := strings.Index(got, "arch-reviewer")
	prIdx := strings.Index(got, "pr-reviewer")
	secIdx := strings.Index(got, "sec-reviewer")
	if !(archIdx < prIdx && prIdx < secIdx) {
		t.Errorf("roster not alphabetical: arch=%d pr=%d sec=%d", archIdx, prIdx, secIdx)
	}
	// Dispatchable agents have [dispatchable] marker.
	if !strings.Contains(got, "pr-reviewer") || !strings.Contains(got, "[dispatchable]") {
		t.Errorf("dispatchable marker missing:\n%s", got)
	}
	// Non-dispatchable agents lack the marker after their entry.
	if strings.Contains(got, "arch-reviewer: Reviews arch (skills: architect) [dispatchable]") {
		t.Errorf("non-dispatchable agent should not have [dispatchable] marker")
	}
}

func TestRenderAgentPromptRosterExcludesSelfFromRoster(t *testing.T) {
	t.Parallel()
	// The current agent ("coder") should not appear in its own roster.
	agent := config.AgentDef{Name: "coder", Prompt: "Code."}
	ctx := ai.PromptContext{
		Repo: "owner/repo",
		Roster: []ai.RosterEntry{
			{Name: "pr-reviewer", Description: "Reviews PRs"},
		},
	}
	got, err := ai.RenderAgentPrompt(agent, nil, ctx)
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	// The RosterEntry for coder is not present (caller omits self from Roster).
	if strings.Contains(got, "**coder**") {
		t.Errorf("current agent should not appear in roster:\n%s", got)
	}
}

func TestRenderAgentPromptRosterOmittedWhenEmpty(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Do X."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if strings.Contains(got, "## Available experts") {
		t.Errorf("empty roster should not produce the experts section:\n%s", got)
	}
}

func TestRenderAgentPromptDispatchContextIncluded(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to dispatch."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{
		Repo:          "owner/repo",
		InvokedBy:     "coder",
		Reason:        "PR is ready for review",
		RootEventID:   "abc-123",
		DispatchDepth: 1,
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	for _, want := range []string{
		"Invoked by: coder",
		"Dispatch reason: PR is ready for review",
		"Root event ID: abc-123",
		"Dispatch depth: 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderAgentPromptDispatchContextOmittedWhenNotDispatched(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Normal run."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	for _, unwanted := range []string{"Invoked by:", "Dispatch reason:", "Dispatch depth:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("dispatch context should be omitted on non-dispatch run; found %q:\n%s", unwanted, got)
		}
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
