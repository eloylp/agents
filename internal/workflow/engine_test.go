package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
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
			"claude": {Agents: []string{"architect"}},
			"codex":  {Agents: []string{"architect"}},
		},
	}
	promptStore := buildTestPrompts(t)
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner, "codex": runner}, promptStore, zerolog.Nop())
	issue := Issue{
		Number: 10,
	}

	err := engine.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:   config.RepoConfig{FullName: "owner/repo", Enabled: true},
		Issue:  issue,
		Action: "labeled",
		Label:  "ai:refine:codex",
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

func buildTestPrompts(t *testing.T) *ai.PromptStore {
	t.Helper()
	dir := t.TempDir()
	issuePath := filepath.Join(dir, "issue_refinement_prompts")
	prPath := filepath.Join(dir, "pr_review_prompts", "architect")
	if err := os.MkdirAll(issuePath, 0o755); err != nil {
		t.Fatalf("create issue prompt dir: %v", err)
	}
	if err := os.MkdirAll(prPath, 0o755); err != nil {
		t.Fatalf("create pr prompt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(issuePath, "PROMPT.md"), []byte("issue {{.Repo}} #{{.Number}}"), 0o644); err != nil {
		t.Fatalf("write issue prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prPath, "PROMPT.md"), []byte("pr {{.Repo}} #{{.Number}} {{.AgentHeading}} {{.WorkflowPartKey}}"), 0o644); err != nil {
		t.Fatalf("write pr prompt: %v", err)
	}
	store, err := ai.NewPromptStore(dir)
	if err != nil {
		t.Fatalf("build prompt store: %v", err)
	}
	return store
}
