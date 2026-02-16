package workflow

import (
	"context"
	"testing"
	"time"

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
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner, "codex": runner}, zerolog.Nop())
	issue := Issue{
		Number:    10,
		Title:     "title",
		Body:      "body",
		UpdatedAt: time.Now().UTC(),
		Labels:    []Label{{Name: "ai:refine:claude"}},
	}

	ran, err := engine.HandleIssueLabelEvent(context.Background(), config.RepoConfig{FullName: "owner/repo", Enabled: true}, issue, "labeled", "ai:refine:codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatalf("expected run for labeled ai event")
	}
	if runner.calls != 1 {
		t.Fatalf("expected one runner call, got %d", runner.calls)
	}
	if runner.last.Workflow != "issue_refine:codex" {
		t.Fatalf("expected event label backend codex, got %q", runner.last.Workflow)
	}
}

func TestHandleIssueLabelEventIgnoresUnlabeledAction(t *testing.T) {
	runner := &stubRunner{}
	cfg := &config.Config{
		AIBackends: map[string]config.AIBackendConfig{
			"claude": {Agents: []string{"architect"}},
		},
	}
	engine := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, zerolog.Nop())
	issue := Issue{
		Number:    10,
		Title:     "title",
		Body:      "body",
		UpdatedAt: time.Now().UTC(),
	}
	ran, err := engine.HandleIssueLabelEvent(context.Background(), config.RepoConfig{FullName: "owner/repo", Enabled: true}, issue, "unlabeled", "ai:refine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran {
		t.Fatalf("expected no run for unlabeled action")
	}
	if runner.calls != 0 {
		t.Fatalf("expected no runner calls, got %d", runner.calls)
	}
}
