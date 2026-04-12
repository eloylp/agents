package workflow

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/ai/testutil"
	"github.com/eloylp/agents/internal/config"
)

type stubRunner struct {
	calls int
	last  ai.Request
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.calls++
	s.last = req
	return ai.Response{Artifacts: []ai.Artifact{{Type: "issue_comment", PartKey: "p", GitHubID: "1"}}}, nil
}

func TestHandleIssueLabelEventUsesPayloadLabel(t *testing.T) {
	runner := &stubRunner{}
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
			"codex":  {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner, "codex": runner}, promptStore, zerolog.Nop())
	issue := Issue{
		Number: 10,
	}

	err := engine.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		Issue: issue,
		Label: "ai:refine:codex",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call, got %d", runner.calls)
	}
	if runner.last.Workflow != "issue_refine:codex" {
		t.Fatalf("expected event label backend codex, got %q", runner.last.Workflow)
	}
}

// TestHandleIssueLabelEventAutoBackend verifies that the "auto" token in a
// label resolves to the default configured backend instead of being treated as
// an unknown backend and silently dropped.
func TestHandleIssueLabelEventAutoBackend(t *testing.T) {
	runner := &stubRunner{}
	// Only claude is configured; "auto" must resolve to it.
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {},
		},
		Agents: []config.AgentConfig{
			{Name: "architect", Skills: []string{"architect"}},
		},
	}
	promptStore := testutil.BuildPromptStore(t, []string{"architect"}, nil)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, promptStore, zerolog.Nop())

	err := engine.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		Issue: Issue{Number: 1},
		Label: "ai:refine:auto",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call for auto backend, got %d", runner.calls)
	}
	if runner.last.Workflow != "issue_refine:claude" {
		t.Fatalf("auto backend should resolve to claude, got workflow %q", runner.last.Workflow)
	}
}
