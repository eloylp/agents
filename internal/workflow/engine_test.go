package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

func (s *stubRunner) lastSystem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[len(s.calls)-1].System
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
	return NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop()), runner
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

func TestEngineDispatchEventPayloadPropagatedToPrompt(t *testing.T) {
	t.Parallel()
	// The engine must pass the full dispatch event payload to the prompt renderer
	// so that the target agent sees target_agent, reason, root_event_id, etc.
	// Dispatch context is per-run, so it must appear in the User part.
	var capturedUser string
	runner := &stubRunner{
		runFn: func(req ai.Request) error {
			capturedUser = req.User
			return nil
		},
	}
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{
				MaxConcurrentAgents: 4,
				Dispatch:            config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300},
			},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]config.SkillDef{},
		Agents: []config.AgentDef{
			{Name: "coder", Backend: "claude", Prompt: "Write code.", AllowDispatch: true},
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review code.", AllowDispatch: true},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "coder", Labels: []string{"ai:code"}},
					{Agent: "pr-reviewer", Labels: []string{"ai:review"}},
				},
			},
		},
	}
	q := &fakeQueue{}
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, q, zerolog.Nop())

	ev := Event{
		ID:     "root-abc",
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "agent.dispatch",
		Number: 7,
		Actor:  "coder",
		Payload: map[string]any{
			"target_agent":   "pr-reviewer",
			"reason":         "please review",
			"root_event_id":  "root-abc",
			"dispatch_depth": 1,
			"invoked_by":     "coder",
		},
	}

	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Fatalf("expected 1 run, got %d", runner.callCount())
	}
	// Dispatch context is per-run content — it must appear in the User part.
	for _, want := range []string{"target_agent", "please review", "root-abc"} {
		if !strings.Contains(capturedUser, want) {
			t.Errorf("User missing %q\nfull User:\n%s", want, capturedUser)
		}
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

func TestEngineAllowPRsFalseInjectsNoPRGuard(t *testing.T) {
	t.Parallel()
	// When allow_prs is false (default), the engine must prepend the no-PR
	// instruction to the system prompt — matching the autonomous scheduler path.
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents[0].AllowPRs = false
	})
	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 1)
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Fatalf("expected 1 run, got %d", runner.callCount())
	}
	const want = "Do not open or create pull requests under any circumstances."
	if !strings.HasPrefix(runner.lastSystem(), want) {
		t.Errorf("system prompt must start with no-PR guard\ngot: %q", runner.lastSystem())
	}
}

func TestEngineAllowPRsTrueOmitsNoPRGuard(t *testing.T) {
	t.Parallel()
	// When allow_prs is true the guard must NOT be prepended.
	e, runner := newTestEngine(func(c *config.Config) {
		c.Agents[0].AllowPRs = true
	})
	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 1)
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Fatalf("expected 1 run, got %d", runner.callCount())
	}
	const noPR = "Do not open or create pull requests under any circumstances."
	if strings.HasPrefix(runner.lastSystem(), noPR) {
		t.Errorf("system prompt must NOT contain no-PR guard when allow_prs=true\ngot: %q", runner.lastSystem())
	}
}

// newTestEngineWithDedup builds an Engine with the dispatch dedup store enabled.
// A non-nil queue is required to activate the Dispatcher; the dedup window is
// set to 60 s so tests stay well within the window.
func newTestEngineWithDedup(cfgMutator func(*config.Config)) (*Engine, *stubRunner, *fakeQueue) {
	runner := &stubRunner{}
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{
				MaxConcurrentAgents: 4,
				Dispatch: config.DispatchConfig{
					MaxDepth:           2,
					MaxFanout:          4,
					DedupWindowSeconds: 60,
				},
			},
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]config.SkillDef{},
		Agents: []config.AgentDef{
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review PR."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "pr-reviewer", Events: []string{"pull_request.synchronize"}},
				},
			},
		},
	}
	if cfgMutator != nil {
		cfgMutator(cfg)
	}
	q := &fakeQueue{}
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, q, zerolog.Nop())
	return e, runner, q
}

// TestFanOutDeduplicatesSequentialEventsWithinTTL verifies that a second event
// for the same (agent, repo, number) arriving within the dedup window is
// suppressed — the claim committed by the first run blocks the second.
func TestFanOutDeduplicatesSequentialEventsWithinTTL(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(nil)
	ev := Event{
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "pull_request.synchronize",
		Number: 42,
	}

	// First event: claim succeeds, run executes.
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("first HandleEvent: %v", err)
	}
	// Second identical event within the TTL window: claim is already committed.
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("second HandleEvent: %v", err)
	}

	if got := runner.callCount(); got != 1 {
		t.Errorf("expected exactly 1 run, got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 1 {
		t.Errorf("expected RunsDeduped=1, got %d", stats.RunsDeduped)
	}
}

// TestFanOutDeduplicatesConcurrentEvents verifies that when two goroutines fire
// the same event concurrently for the same (agent, repo, number), exactly one
// agent run completes and the other is suppressed. The TryClaim inside the
// goroutine is mutex-protected, so exactly one caller wins regardless of
// goroutine scheduling order.
func TestFanOutDeduplicatesConcurrentEvents(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(nil)
	ev := Event{
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "pull_request.synchronize",
		Number: 42,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			_ = e.HandleEvent(context.Background(), ev)
		}()
	}
	wg.Wait()

	if got := runner.callCount(); got != 1 {
		t.Errorf("expected exactly 1 run from concurrent events, got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 1 {
		t.Errorf("expected RunsDeduped=1, got %d", stats.RunsDeduped)
	}
}

// TestFanOutDifferentNumbersAreNotDeduped verifies that events for different
// (agent, repo, number) keys each get their own run — dedup must not
// collapse distinct items under the same agent/repo umbrella.
func TestFanOutDifferentNumbersAreNotDeduped(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(nil)

	for _, number := range []int{1, 2, 3} {
		ev := Event{
			Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
			Kind:   "pull_request.synchronize",
			Number: number,
		}
		if err := e.HandleEvent(context.Background(), ev); err != nil {
			t.Fatalf("HandleEvent(number=%d): %v", number, err)
		}
	}

	if got := runner.callCount(); got != 3 {
		t.Errorf("expected 3 runs for 3 distinct numbers, got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 0 {
		t.Errorf("expected RunsDeduped=0, got %d", stats.RunsDeduped)
	}
}

// TestFanOutClaimAbandonedOnRunFailure verifies that a failed agent run
// releases the pending dedup claim so that a subsequent event for the same
// (agent, repo, number) is allowed to proceed.
func TestFanOutClaimAbandonedOnRunFailure(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(nil)
	ev := Event{
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "pull_request.synchronize",
		Number: 42,
	}

	// First run fails.
	runErr := errors.New("backend error")
	runner.runFn = func(_ ai.Request) error { return runErr }
	if err := e.HandleEvent(context.Background(), ev); err == nil {
		t.Fatal("expected error from first run, got nil")
	}

	// Second run succeeds: the abandoned claim must have been released.
	runner.runFn = nil
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("second HandleEvent after failure: %v", err)
	}

	// Both attempts ran; dedup did not suppress the retry.
	if got := runner.callCount(); got != 2 {
		t.Errorf("expected 2 runs (failure + retry), got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 0 {
		t.Errorf("expected RunsDeduped=0 (retry should not be counted as deduped), got %d", stats.RunsDeduped)
	}
}

// TestFanOutDoesNotDedupZeroNumberEvents verifies that repo-level events with
// number=0 (e.g. push) are never collapsed by the dedup gate.  Two distinct
// pushes to the same repo must each trigger their bound agent.
func TestFanOutDoesNotDedupZeroNumberEvents(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "pusher", Backend: "claude", Prompt: "React to pushes."})
		c.Repos[0].Use = append(c.Repos[0].Use, config.Binding{Agent: "pusher", Events: []string{"push"}})
	})

	push1 := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "push",
		Number:  0,
		Payload: map[string]any{"head_sha": "abc123"},
	}
	push2 := Event{
		Repo:    RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:    "push",
		Number:  0,
		Payload: map[string]any{"head_sha": "def456"},
	}

	if err := e.HandleEvent(context.Background(), push1); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := e.HandleEvent(context.Background(), push2); err != nil {
		t.Fatalf("second push: %v", err)
	}

	if got := runner.callCount(); got != 2 {
		t.Errorf("expected 2 runs for two distinct pushes, got %d (second push must not be deduplicated)", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 0 {
		t.Errorf("expected RunsDeduped=0 for push events, got %d", stats.RunsDeduped)
	}
}

// TestDispatchEventRunsAfterEnqueue is an end-to-end regression test for the
// dispatch self-suppression bug: ProcessDispatches commits the dedup claim
// before enqueuing, so handleDispatchEvent must NOT re-claim or the agent is
// silently dropped. This test goes through the real enqueue→dequeue path.
func TestDispatchEventRunsAfterEnqueue(t *testing.T) {
	t.Parallel()
	e, runner, q := newTestEngineWithDedup(func(c *config.Config) {
		c.Agents[0].AllowDispatch = true
		c.Agents = append(c.Agents, config.AgentDef{
			Name:          "coder",
			Backend:       "claude",
			Prompt:        "Write code.",
			AllowDispatch: true,
			CanDispatch:   []string{"pr-reviewer"},
		})
	})

	originator := config.AgentDef{
		Name:        "coder",
		CanDispatch: []string{"pr-reviewer"},
	}
	triggerEv := Event{
		ID:     "root-1",
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "issues.labeled",
		Number: 7,
		Payload: map[string]any{"label": "ai:code"},
	}
	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 7, Reason: "ready"}}

	// ProcessDispatches enqueues the agent.dispatch event and commits its claim.
	if err := e.Dispatcher().ProcessDispatches(context.Background(), originator, triggerEv, "root-1", 0, "", reqs); err != nil {
		t.Fatalf("ProcessDispatches: %v", err)
	}
	enqueued := q.popped()
	if len(enqueued) != 1 {
		t.Fatalf("expected 1 enqueued event, got %d", len(enqueued))
	}

	// HandleEvent processes the dequeued agent.dispatch event.
	// Before the fix this returned nil but suppressed the run (self-suppression).
	if err := e.HandleEvent(context.Background(), enqueued[0]); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Errorf("expected 1 agent run after dequeue, got %d (dispatch self-suppression regression)", got)
	}
}

// TestDispatchDedupPreventsDoubleEnqueue verifies that the enqueue-side dedup
// in ProcessDispatches prevents a second identical dispatch from being enqueued
// within the TTL window. The dedup gate belongs at enqueue, not at execution.
func TestDispatchDedupPreventsDoubleEnqueue(t *testing.T) {
	t.Parallel()
	e, _, q := newTestEngineWithDedup(func(c *config.Config) {
		c.Agents[0].AllowDispatch = true
		c.Agents = append(c.Agents, config.AgentDef{
			Name:          "coder",
			Backend:       "claude",
			Prompt:        "Write code.",
			AllowDispatch: true,
			CanDispatch:   []string{"pr-reviewer"},
		})
	})

	originator := config.AgentDef{
		Name:        "coder",
		CanDispatch: []string{"pr-reviewer"},
	}
	triggerEv := Event{
		ID:     "root-2",
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "issues.labeled",
		Number: 9,
		Payload: map[string]any{"label": "ai:code"},
	}
	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 9, Reason: "first"}}

	// First call enqueues the event.
	if err := e.Dispatcher().ProcessDispatches(context.Background(), originator, triggerEv, "root-2", 0, "", reqs); err != nil {
		t.Fatalf("first ProcessDispatches: %v", err)
	}
	// Second identical call within TTL must be suppressed at enqueue time.
	reqs2 := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 9, Reason: "duplicate"}}
	if err := e.Dispatcher().ProcessDispatches(context.Background(), originator, triggerEv, "root-2", 0, "", reqs2); err != nil {
		t.Fatalf("second ProcessDispatches: %v", err)
	}

	if got := len(q.popped()); got != 1 {
		t.Errorf("expected exactly 1 enqueued event (dedup suppressed second), got %d", got)
	}
	if stats := e.Dispatcher().Stats(); stats.Deduped != 1 {
		t.Errorf("expected Deduped=1 from enqueue-side dedup, got %d", stats.Deduped)
	}
}

// TestFanOutDedupSurvivesTTLExpiry is a regression test for the in-flight
// refcount fix: a second identical event arriving after dedup_window_seconds
// has elapsed — but while the first run is still executing — must be suppressed.
// Before the fix, TryClaimForDispatch only checked the TTL entry; once the
// entry expired the second event could claim the slot and start a concurrent
// duplicate run.
func TestFanOutDedupSurvivesTTLExpiry(t *testing.T) {
	t.Parallel()

	// Use an engine with a 1-second dedup window so the TTL expires quickly.
	e, runner, _ := newTestEngineWithDedup(func(c *config.Config) {
		c.Daemon.Processor.Dispatch.DedupWindowSeconds = 1
	})

	var (
		runStarted = make(chan struct{})
		unblock    = make(chan struct{})
	)
	// The first run blocks until we release it, simulating a long-running agent.
	runner.runFn = func(_ ai.Request) error {
		close(runStarted)
		<-unblock
		return nil
	}

	ev := Event{
		Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:   "pull_request.synchronize",
		Number: 42,
	}

	// Start first run in background; it will block until unblock is closed.
	firstDone := make(chan error, 1)
	go func() { firstDone <- e.HandleEvent(context.Background(), ev) }()

	// Wait until the first run has started and is in-flight.
	<-runStarted

	// Wait for the TTL window to expire so the store entry expires.
	time.Sleep(1500 * time.Millisecond)

	// Fire a second identical event while the first run is still in-flight.
	// The in-flight refcount must suppress it even though the TTL has expired.
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("second HandleEvent: %v", err)
	}

	// Release the first run.
	close(unblock)
	if err := <-firstDone; err != nil {
		t.Fatalf("first HandleEvent: %v", err)
	}

	if got := runner.callCount(); got != 1 {
		t.Errorf("expected exactly 1 run (second suppressed by in-flight refcount), got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 1 {
		t.Errorf("expected RunsDeduped=1, got %d", stats.RunsDeduped)
	}
}
