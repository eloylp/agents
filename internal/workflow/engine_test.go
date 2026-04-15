package workflow

import (
	"context"
	"errors"
	"strings"
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

// newTestEngine builds an Engine with a canned agent set. The cfgMutator
// hook lets tests override bindings, backends, etc.
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

// labelEvent builds an Event for a labeled trigger (issues or pull_request).
func labelEvent(kind, repo, label string, number int) Event {
	return Event{
		Repo:    RepoRef{FullName: repo, Enabled: true},
		Kind:    kind,
		Number:  number,
		Payload: map[string]any{"label": label},
	}
}

func TestHandleEventIssueRunsMatchingLabelBinding(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "refiner", Backend: "claude", Prompt: "Refine the issue."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "refiner", Labels: []string{"ai:refine"}})
	})
	err := e.HandleEvent(context.Background(), labelEvent("issues.labeled", "owner/repo", "ai:refine", 7))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandleEventPRRunsSingleLabelBinding(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(nil)
	err := e.HandleEvent(context.Background(), labelEvent("pull_request.labeled", "owner/repo", "ai:review:arch-reviewer", 1))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandleEventFansOutToMultipleLabelBindings(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Repos[0].Use = []config.Binding{
			{Agent: "arch-reviewer", Labels: []string{"ai:review:all"}},
			{Agent: "sec-reviewer", Labels: []string{"ai:review:all"}},
		}
	})
	err := e.HandleEvent(context.Background(), labelEvent("issues.labeled", "owner/repo", "ai:review:all", 1))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 2 {
		t.Errorf("expected 2 runs for fan-out, got %d", runner.callCount())
	}
}

func TestEngineSkipsUnmatchedLabel(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(nil)
	err := e.HandleEvent(context.Background(), labelEvent("issues.labeled", "owner/repo", "ai:review:no-such-agent", 1))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
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
	err := e.HandleEvent(context.Background(), labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 1))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
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
		if strings.Contains(req.Workflow, "sec-reviewer") {
			return boom
		}
		return nil
	}
	err := e.HandleEvent(context.Background(), labelEvent("issues.labeled", "owner/repo", "ai:review:all", 1))
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected joined error containing boom, got %v", err)
	}
	// First agent still ran successfully.
	if runner.callCount() != 2 {
		t.Errorf("expected both agents to be attempted, got %d calls", runner.callCount())
	}
}

func TestHandleEventEventBindingMatchesKind(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "commenter", Backend: "claude", Prompt: "React to comments."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "commenter", Events: []string{"issue_comment.created"}})
	})
	ev := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "issue_comment.created",
		Number:  3,
		Actor:   "octocat",
		Payload: map[string]any{"body": "LGTM"},
	}
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandleEventEventBindingDoesNotMatchWrongKind(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "pusher", Backend: "claude", Prompt: "React to pushes."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "pusher", Events: []string{"push"}})
	})
	// issue_comment.created should NOT match a push binding.
	ev := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "issue_comment.created",
		Number:  3,
		Payload: map[string]any{"body": "hello"},
	}
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 0 {
		t.Errorf("expected 0 runs, got %d", runner.callCount())
	}
}

func TestHandleEventPushEventBindingRuns(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "pusher", Backend: "claude", Prompt: "React to pushes."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "pusher", Events: []string{"push"}})
	})
	ev := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "push",
		Actor:   "dev",
		Payload: map[string]any{"ref": "refs/heads/main", "head_sha": "abc123"},
	}
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}

func TestHandleEventLabelBindingDoesNotMatchNonLabeledKind(t *testing.T) {
	t.Parallel()
	// arch-reviewer has a label binding; a push event must not trigger it.
	e, runner := newTestEngine(nil)
	ev := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "push",
		Payload: map[string]any{"ref": "refs/heads/main"},
	}
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 0 {
		t.Errorf("label binding must not fire on non-labeled event; got %d runs", runner.callCount())
	}
}

func TestHandleEventPRReviewEventBindingRuns(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "reviewer", Backend: "claude", Prompt: "React to reviews."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "reviewer", Events: []string{"pull_request_review.submitted"}})
	})
	ev := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "pull_request_review.submitted",
		Number:  5,
		Actor:   "reviewer-bot",
		Payload: map[string]any{"state": "changes_requested", "body": "Please fix X"},
	}
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run, got %d", runner.callCount())
	}
}
