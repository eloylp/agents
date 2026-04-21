package ai_test

import (
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

// renderSystem is a test helper that returns the System part of a rendered
// prompt or fails the test if rendering fails.
func renderSystem(t *testing.T, agent config.AgentDef, skills map[string]config.SkillDef, ctx ai.PromptContext) string {
	t.Helper()
	got, err := ai.RenderAgentPrompt(agent, skills, ctx)
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	return got.System
}

// renderUser is a test helper that returns the User part of a rendered prompt
// or fails the test if rendering fails.
func renderUser(t *testing.T, agent config.AgentDef, skills map[string]config.SkillDef, ctx ai.PromptContext) string {
	t.Helper()
	got, err := ai.RenderAgentPrompt(agent, skills, ctx)
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	return got.User
}

func TestRenderAgentPromptSkillsAndPromptInSystem(t *testing.T) {
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

	// Skills must appear in list order in the System part, before the agent prompt.
	sys := got.System
	if !strings.Contains(sys, "Focus on architecture.") {
		t.Errorf("missing architect guidance in System:\n%s", sys)
	}
	archIdx := strings.Index(sys, "Focus on architecture.")
	testIdx := strings.Index(sys, "Focus on tests.")
	promptIdx := strings.Index(sys, "You review PRs.")
	if !(archIdx < testIdx && testIdx < promptIdx) {
		t.Errorf("ordering wrong: arch=%d test=%d prompt=%d", archIdx, testIdx, promptIdx)
	}

	// Runtime context must be in the User part, not the System part.
	usr := got.User
	if strings.Contains(sys, "Repository: owner/repo") {
		t.Errorf("runtime context must not appear in System:\n%s", sys)
	}
	if !strings.Contains(usr, "Repository: owner/repo") {
		t.Errorf("missing repo context in User:\n%s", usr)
	}
	if !strings.Contains(usr, "Issue/PR number: 42") {
		t.Errorf("missing issue number in User:\n%s", usr)
	}
}

func TestRenderAgentPromptSystemAndUserAreSeparate(t *testing.T) {
	t.Parallel()
	// Skills and agent prompt must only appear in System; runtime context must
	// only appear in User.
	skills := map[string]config.SkillDef{
		"sec": {Prompt: "Check security."},
	}
	agent := config.AgentDef{Name: "sec-reviewer", Skills: []string{"sec"}, Prompt: "Audit the code."}
	got, err := ai.RenderAgentPrompt(agent, skills, ai.PromptContext{
		Repo: "owner/repo", Number: 7, EventKind: "issues.labeled", Actor: "bot",
	})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}

	for _, stableToken := range []string{"Check security.", "Audit the code."} {
		if !strings.Contains(got.System, stableToken) {
			t.Errorf("stable token %q missing from System:\n%s", stableToken, got.System)
		}
		if strings.Contains(got.User, stableToken) {
			t.Errorf("stable token %q must not appear in User:\n%s", stableToken, got.User)
		}
	}
	for _, runtimeToken := range []string{"owner/repo", "Issue/PR number: 7", "issues.labeled", "Actor: bot"} {
		if strings.Contains(got.System, runtimeToken) {
			t.Errorf("runtime token %q must not appear in System:\n%s", runtimeToken, got.System)
		}
		if !strings.Contains(got.User, runtimeToken) {
			t.Errorf("runtime token %q missing from User:\n%s", runtimeToken, got.User)
		}
	}
}

func TestRenderAgentPromptWithMemory(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{
		Name:   "autonomous",
		Prompt: "Do your job.",
	}
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:         "owner/repo",
		IsAutonomous: true,
		Memory:       "Last run: fixed #42",
	})
	if strings.Contains(usr, "Memory file:") {
		t.Errorf("memory path should not appear in User:\n%s", usr)
	}
	if !strings.Contains(usr, "Last run: fixed #42") {
		t.Errorf("missing memory content in User:\n%s", usr)
	}
}

func TestRenderAgentPromptEmptyMemoryFormattedExplicitly(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Go."}
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:         "owner/repo",
		IsAutonomous: true,
	})
	if !strings.Contains(usr, "Existing memory: (empty)") {
		t.Errorf("empty memory not signalled explicitly in User:\n%s", usr)
	}
}

func TestRenderAgentPromptMemoryOmittedForEventDrivenRuns(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Review this PR."}
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:   "owner/repo",
		Number: 42,
		// IsAutonomous is false — event-driven run, no memory section expected.
		Memory: "some content that should not appear",
	})
	if strings.Contains(usr, "Existing memory") {
		t.Errorf("memory section must not appear in event-driven runs:\n%s", usr)
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

func TestRenderAgentPromptOmitsUserWhenContextEmpty(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Do X."}
	got, err := ai.RenderAgentPrompt(agent, nil, ai.PromptContext{})
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	if strings.Contains(got.User, "## Runtime context") {
		t.Errorf("runtime section should be omitted when context is empty:\n%s", got.User)
	}
	if got.User != "" {
		t.Errorf("User should be empty when context is empty, got:\n%s", got.User)
	}
}

func TestRenderAgentPromptIncludesEventContextInUser(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to comments."}
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:      "owner/repo",
		Number:    3,
		EventKind: "issue_comment.created",
		Actor:     "octocat",
		Payload:   map[string]any{"body": "LGTM"},
	})
	if !strings.Contains(usr, "Event: issue_comment.created") {
		t.Errorf("missing event kind in User:\n%s", usr)
	}
	if !strings.Contains(usr, "Actor: octocat") {
		t.Errorf("missing actor in User:\n%s", usr)
	}
	if !strings.Contains(usr, "body: LGTM") {
		t.Errorf("missing payload body in User:\n%s", usr)
	}
}

func TestRenderAgentPromptMultilinePayloadBodyIsIndented(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to comments."}
	body := "first line\nsecond line\nthird line"
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:      "owner/repo",
		Number:    7,
		EventKind: "issue_comment.created",
		Actor:     "octocat",
		Payload:   map[string]any{"body": body},
	})
	// Multiline values must be rendered as indented block, not bare continuation lines.
	if strings.Contains(usr, "body: first line") {
		t.Errorf("multiline body rendered inline (not indented):\n%s", usr)
	}
	if !strings.Contains(usr, "body:\n  first line\n  second line\n  third line") {
		t.Errorf("multiline body not rendered as indented block:\n%s", usr)
	}
}

func TestRenderAgentPromptRosterInUser(t *testing.T) {
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
	usr := got.User
	if !strings.Contains(usr, "## Available experts") {
		t.Errorf("missing experts section in User:\n%s", usr)
	}
	// Roster must not bleed into System.
	if strings.Contains(got.System, "Available experts") {
		t.Errorf("roster section must not appear in System:\n%s", got.System)
	}
	archIdx := strings.Index(usr, "arch-reviewer")
	prIdx := strings.Index(usr, "pr-reviewer")
	secIdx := strings.Index(usr, "sec-reviewer")
	if !(archIdx < prIdx && prIdx < secIdx) {
		t.Errorf("roster not alphabetical: arch=%d pr=%d sec=%d", archIdx, prIdx, secIdx)
	}
	if !strings.Contains(usr, "pr-reviewer") || !strings.Contains(usr, "[dispatchable]") {
		t.Errorf("dispatchable marker missing:\n%s", usr)
	}
	if strings.Contains(usr, "arch-reviewer: Reviews arch (skills: architect) [dispatchable]") {
		t.Errorf("non-dispatchable agent should not have [dispatchable] marker")
	}
}

func TestRenderAgentPromptRosterExcludesSelfFromRoster(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Name: "coder", Prompt: "Code."}
	ctx := ai.PromptContext{
		Repo: "owner/repo",
		Roster: []ai.RosterEntry{
			{Name: "pr-reviewer", Description: "Reviews PRs"},
		},
	}
	usr := renderUser(t, agent, nil, ctx)
	if strings.Contains(usr, "**coder**") {
		t.Errorf("current agent should not appear in roster:\n%s", usr)
	}
}

func TestRenderAgentPromptRosterOmittedWhenEmpty(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Do X."}
	usr := renderUser(t, agent, nil, ai.PromptContext{Repo: "owner/repo"})
	if strings.Contains(usr, "## Available experts") {
		t.Errorf("empty roster should not produce the experts section:\n%s", usr)
	}
}

func TestRenderAgentPromptDispatchContextInUser(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "React to dispatch."}
	usr := renderUser(t, agent, nil, ai.PromptContext{
		Repo:          "owner/repo",
		InvokedBy:     "coder",
		Reason:        "PR is ready for review",
		RootEventID:   "abc-123",
		DispatchDepth: 1,
	})
	for _, want := range []string{
		"Invoked by: coder",
		"Dispatch reason: PR is ready for review",
		"Root event ID: abc-123",
		"Dispatch depth: 1",
	} {
		if !strings.Contains(usr, want) {
			t.Errorf("missing %q in User:\n%s", want, usr)
		}
	}
}

func TestRenderAgentPromptDispatchContextOmittedWhenNotDispatched(t *testing.T) {
	t.Parallel()
	agent := config.AgentDef{Prompt: "Normal run."}
	usr := renderUser(t, agent, nil, ai.PromptContext{Repo: "owner/repo"})
	for _, unwanted := range []string{"Invoked by:", "Dispatch reason:", "Dispatch depth:"} {
		if strings.Contains(usr, unwanted) {
			t.Errorf("dispatch context should be omitted on non-dispatch run; found %q:\n%s", unwanted, usr)
		}
	}
}

func TestNormalizeTokenSanitizesForFilesystemUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"lowercase", "Architect", "architect"},
		{"trim_spaces", "  Foo Bar  ", "foo bar"},
		{"strip_dotdot", "../evil", "evil"},
		{"slash_to_underscore", "a/b/c", "a_b_c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ai.NormalizeToken(tt.in); got != tt.want {
				t.Errorf("NormalizeToken(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRenderAgentPromptTotalContentPreserved verifies that the combined
// System+User content contains all original tokens — no content is dropped by
// the split. This is the regression guard required by the issue.
func TestRenderAgentPromptTotalContentPreserved(t *testing.T) {
	t.Parallel()
	skills := map[string]config.SkillDef{
		"arch": {Prompt: "Architecture guidance."},
	}
	agent := config.AgentDef{Name: "coder", Skills: []string{"arch"}, Prompt: "Write code."}
	ctx := ai.PromptContext{
		Repo:      "owner/repo",
		Number:    5,
		EventKind: "issues.labeled",
		Actor:     "dev",
	}
	got, err := ai.RenderAgentPrompt(agent, skills, ctx)
	if err != nil {
		t.Fatalf("RenderAgentPrompt: %v", err)
	}
	combined := got.System + "\n\n" + got.User
	for _, token := range []string{
		"Architecture guidance.", "Write code.",
		"owner/repo", "Issue/PR number: 5", "issues.labeled", "Actor: dev",
	} {
		if !strings.Contains(combined, token) {
			t.Errorf("token %q lost after System+User split; combined:\n%s", token, combined)
		}
	}
}
