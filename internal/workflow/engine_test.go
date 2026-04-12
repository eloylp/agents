package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
)

type stubRunner struct {
	mu    sync.Mutex
	calls []ai.Request
	runFn func(ai.Request) error
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	if s.runFn != nil {
		if err := s.runFn(req); err != nil {
			return ai.Response{}, err
		}
	}
	return ai.Response{}, nil
}

func (s *stubRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubRunner) agentNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		// The agent name is the last colon-separated part of the workflow tag.
		out = append(out, c.Workflow)
	}
	return out
}

// newTestEngine builds an Engine with a canned agent set. The cfgMutator
// hook lets tests override bindings, drafts, etc.
func newTestEngine(cfgMutator func(*config.Config)) (*Engine, *stubRunner) {
	runner := &stubRunner{}
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{MaxConcurrentAgents: 4},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]config.SkillDef{
			"architect": {Prompt: "Focus on architecture."},
			"security":  {Prompt: "Focus on security."},
		},
		Agents: []config.AgentDef{
			{Name: "arch-reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review architecture."},
			{Name: "sec-reviewer", Backend: "claude", Skills: []string{"security"}, Prompt: "Review security."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "arch-reviewer", Labels: []string{"ai:review:arch-reviewer"}},
					{Agent: "sec-reviewer", Labels: []string{"ai:review:sec-reviewer"}},
				},
			},
		},
	}
	if cfgMutator != nil {
		cfgMutator(cfg)
	}
	return NewEngine(cfg, map[string]ai.Runner{"claude": runner}, zerolog.Nop()), runner
}

func TestHandleIssueLabelEventRunsMatchingBinding(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		// Add an issue-style binding.
		c.Agents = append(c.Agents, config.AgentDef{Name: "refiner", Backend: "claude", Prompt: "Refine the issue."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "refiner", Labels: []string{"ai:refine"}})
	})
	err := e.HandleIssueLabelEvent(context.Background(), IssueRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		Issue: Issue{Number: 7},
		Label: "ai:refine",
	})
	if err != nil {
		t.Fatalf("HandleIssueLabelEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandlePullRequestLabelEventRunsSingleAgent(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(nil)
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1},
		Label: "ai:review:arch-reviewer",
	})
	if err != nil {
		t.Fatalf("HandlePullRequestLabelEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandlePullRequestLabelEventFansOutToMultipleBindings(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		// Wire both reviewers to the same shared label.
		c.Repos[0].Use = []config.Binding{
			{Agent: "arch-reviewer", Labels: []string{"ai:review:all"}},
			{Agent: "sec-reviewer", Labels: []string{"ai:review:all"}},
		}
	})
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1},
		Label: "ai:review:all",
	})
	if err != nil {
		t.Fatalf("HandlePullRequestLabelEvent: %v", err)
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 runs for fan-out, got %d", runner.callCount())
	}
}

func TestHandlePullRequestLabelEventSkipsDraftPRs(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(nil)
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1, Draft: true},
		Label: "ai:review:arch-reviewer",
	})
	if err != nil {
		t.Fatalf("HandlePullRequestLabelEvent: %v", err)
	}
	if runner.callCount() != 0 {
		t.Errorf("expected 0 runs for draft PR, got %d", runner.callCount())
	}
}

func TestEngineSkipsUnmatchedLabel(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(nil)
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1},
		Label: "ai:review:no-such-agent",
	})
	if err != nil {
		t.Fatalf("HandlePullRequestLabelEvent: %v", err)
	}
	if runner.callCount() != 0 {
		t.Errorf("expected 0 runs, got %d", runner.callCount())
	}
}

func TestEngineSkipsDisabledBinding(t *testing.T) {
	t.Parallel()
	f := false
	e, runner := newTestEngine(func(c *config.Config) {
		c.Repos[0].Use[0].Enabled = &f
	})
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1},
		Label: "ai:review:arch-reviewer",
	})
	if err != nil {
		t.Fatalf("HandlePullRequestLabelEvent: %v", err)
	}
	if runner.callCount() != 0 {
		t.Errorf("expected 0 runs for disabled binding, got %d", runner.callCount())
	}
}

func TestEngineJoinsErrorsAcrossAgents(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	e, runner := newTestEngine(func(c *config.Config) {
		c.Repos[0].Use = []config.Binding{
			{Agent: "arch-reviewer", Labels: []string{"ai:review:all"}},
			{Agent: "sec-reviewer", Labels: []string{"ai:review:all"}},
		}
	})
	runner.runFn = func(req ai.Request) error {
		if contains(req.Workflow, "sec-reviewer") {
			return boom
		}
		return nil
	}
	err := e.HandlePullRequestLabelEvent(context.Background(), PRRequest{
		Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
		PR:    PullRequest{Number: 1},
		Label: "ai:review:all",
	})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected joined error containing boom, got %v", err)
	}
	// First agent still ran successfully.
	if runner.callCount() != 2 {
		t.Errorf("expected both agents to be attempted, got %d calls", runner.callCount())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
