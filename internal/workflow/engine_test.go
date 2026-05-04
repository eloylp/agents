package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

type stubRunner struct {
	mu     sync.Mutex
	calls  []ai.Request
	runFn  func(ai.Request) error
	respFn func(ai.Request) ai.Response
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	respFn := s.respFn
	s.mu.Unlock()
	if s.runFn != nil {
		if err := s.runFn(req); err != nil {
			return ai.Response{}, err
		}
	}
	if respFn != nil {
		return respFn(req), nil
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
func newTestEngine(t *testing.T, cfgMutator func(*config.Config)) (*Engine, *stubRunner) {
	t.Helper()
	runner := &stubRunner{}
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{MaxConcurrentAgents: 4},
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]fleet.Skill{
			"architect": {Prompt: "Focus on architecture."},
			"security":  {Prompt: "Focus on security."},
		},
		Agents: []fleet.Agent{
			{Name: "arch-reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review architecture."},
			{Name: "sec-reviewer", Backend: "claude", Skills: []string{"security"}, Prompt: "Review security."},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "arch-reviewer", Labels: []string{"ai:review:arch-reviewer"}},
					{Agent: "sec-reviewer", Labels: []string{"ai:review:sec-reviewer"}},
				},
			},
		},
	}
	if cfgMutator != nil {
		cfgMutator(cfg)
	}
	return newEngineFromCfg(t, cfg, map[string]ai.Runner{"claude": runner}, nil), runner
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
	e, runner := newTestEngine(t, func(c *config.Config) {
		c.Agents = append(c.Agents, fleet.Agent{Name: "refiner", Backend: "claude", Prompt: "Refine the issue."})
		c.Repos[0].Use = append(c.Repos[0].Use, fleet.Binding{Agent: "refiner", Labels: []string{"ai:refine"}})
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
	e, runner := newTestEngine(t, nil)
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
	e, runner := newTestEngine(t, func(c *config.Config) {
		c.Repos[0].Use = []fleet.Binding{
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
	e, runner := newTestEngine(t, nil)
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
	e, runner := newTestEngine(t, func(c *config.Config) {
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
	e, runner := newTestEngine(t, func(c *config.Config) {
		c.Repos[0].Use = []fleet.Binding{
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

func TestHandleEventEventBindings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		bindEvent    string
		triggerEvent string
		wantRuns     int
	}{
		{
			name:         "matching kind fires agent",
			bindEvent:    "issue_comment.created",
			triggerEvent: "issue_comment.created",
			wantRuns:     1,
		},
		{
			name:         "mismatched kind skips agent",
			bindEvent:    "push",
			triggerEvent: "issue_comment.created",
			wantRuns:     0,
		},
		{
			name:         "push binding fires on push",
			bindEvent:    "push",
			triggerEvent: "push",
			wantRuns:     1,
		},
		{
			name:         "pr_review_submitted binding fires on pr_review_submitted",
			bindEvent:    "pull_request_review.submitted",
			triggerEvent: "pull_request_review.submitted",
			wantRuns:     1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, runner := newTestEngine(t, func(c *config.Config) {
				c.Agents = append(c.Agents, fleet.Agent{Name: "watcher", Backend: "claude", Prompt: "React to events."})
				c.Repos[0].Use = append(c.Repos[0].Use, fleet.Binding{Agent: "watcher", Events: []string{tc.bindEvent}})
			})
			ev := Event{
				Repo:   RepoRef{FullName: "owner/repo", Enabled: true},
				Kind:   tc.triggerEvent,
				Number: 3,
			}
			if err := e.HandleEvent(context.Background(), ev); err != nil {
				t.Fatalf("HandleEvent: %v", err)
			}
			if runner.callCount() != tc.wantRuns {
				t.Errorf("callCount = %d, want %d", runner.callCount(), tc.wantRuns)
			}
		})
	}
}

func TestHandleEventLabelBindingDoesNotMatchNonLabeledKind(t *testing.T) {
	t.Parallel()
	// arch-reviewer has a label binding; a push event must not trigger it.
	e, runner := newTestEngine(t, nil)
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
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]fleet.Skill{},
		Agents: []fleet.Agent{
			{Name: "coder", Backend: "claude", Prompt: "Write code.", AllowDispatch: true},
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review code.", AllowDispatch: true},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "coder", Labels: []string{"ai:code"}},
					{Agent: "pr-reviewer", Labels: []string{"ai:review"}},
				},
			},
		},
	}
	q := &fakeQueue{}
	e := newEngineFromCfg(t, cfg, map[string]ai.Runner{"claude": runner}, q)

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
	// Dispatch context is per-run content, it must appear in the User part.
	for _, want := range []string{"target_agent", "please review", "root-abc"} {
		if !strings.Contains(capturedUser, want) {
			t.Errorf("User missing %q\nfull User:\n%s", want, capturedUser)
		}
	}
}

func TestEngineAllowPRsFalseInjectsNoPRGuard(t *testing.T) {
	t.Parallel()
	const noPRGuard = "Do not open or create pull requests under any circumstances."
	tests := []struct {
		name        string
		allowPRs    bool
		wantGuard   bool
	}{
		{
			name:      "guard prepended when allow_prs=false",
			allowPRs:  false,
			wantGuard: true,
		},
		{
			name:      "guard absent when allow_prs=true",
			allowPRs:  true,
			wantGuard: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, runner := newTestEngine(t, func(c *config.Config) {
				c.Agents[0].AllowPRs = tc.allowPRs
			})
			ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 1)
			if err := e.HandleEvent(context.Background(), ev); err != nil {
				t.Fatalf("HandleEvent: %v", err)
			}
			if runner.callCount() != 1 {
				t.Fatalf("expected 1 run, got %d", runner.callCount())
			}
			// Guardrails (when seeded) sit in front of the no-PR guard, so a
			// prefix check would fail in any test environment that applied the
			// guardrails migration. Contains captures the intent: is the guard
			// in the System block when allow_prs=false?
			hasGuard := strings.Contains(runner.lastSystem(), noPRGuard)
			if hasGuard != tc.wantGuard {
				t.Errorf("no-PR guard present=%v, want %v\nsystem: %q", hasGuard, tc.wantGuard, runner.lastSystem())
			}
		})
	}
}

// newTestEngineWithDedup builds an Engine with the dispatch dedup store enabled.
// A non-nil queue is required to activate the Dispatcher; the dedup window is
// set to 60 s so tests stay well within the window.
func newTestEngineWithDedup(t *testing.T, cfgMutator func(*config.Config)) (*Engine, *stubRunner, *fakeQueue) {
	t.Helper()
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
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]fleet.Skill{},
		Agents: []fleet.Agent{
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review PR.", Description: "Reviews PRs", AllowDispatch: true},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "pr-reviewer", Events: []string{"pull_request.synchronize"}},
				},
			},
		},
	}
	if cfgMutator != nil {
		cfgMutator(cfg)
	}
	q := &fakeQueue{}
	e := newEngineFromCfg(t, cfg, map[string]ai.Runner{"claude": runner}, q)
	return e, runner, q
}

// TestFanOutDeduplicatesSequentialEventsWithinTTL verifies that a second event
// for the same (agent, repo, number) arriving within the dedup window is
// suppressed, the claim committed by the first run blocks the second.
func TestFanOutDeduplicatesSequentialEventsWithinTTL(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(t, nil)
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
	e, runner, _ := newTestEngineWithDedup(t, nil)
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
// (agent, repo, number) keys each get their own run, dedup must not
// collapse distinct items under the same agent/repo umbrella.
func TestFanOutDifferentNumbersAreNotDeduped(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(t, nil)

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
	e, runner, _ := newTestEngineWithDedup(t, nil)
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
	e, runner, _ := newTestEngineWithDedup(t, func(c *config.Config) {
		c.Agents = append(c.Agents, fleet.Agent{Name: "pusher", Backend: "claude", Prompt: "React to pushes."})
		c.Repos[0].Use = append(c.Repos[0].Use, fleet.Binding{Agent: "pusher", Events: []string{"push"}})
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
	e, runner, q := newTestEngineWithDedup(t, func(c *config.Config) {
		c.Agents[0].AllowDispatch = true
		c.Agents = append(c.Agents, fleet.Agent{
			Name:          "coder",
			Backend:       "claude",
			Prompt:        "Write code.",
			AllowDispatch: true,
			CanDispatch:   []string{"pr-reviewer"},
		})
	})

	originator := fleet.Agent{
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
	e, _, q := newTestEngineWithDedup(t, func(c *config.Config) {
		c.Agents[0].AllowDispatch = true
		c.Agents = append(c.Agents, fleet.Agent{
			Name:          "coder",
			Backend:       "claude",
			Prompt:        "Write code.",
			AllowDispatch: true,
			CanDispatch:   []string{"pr-reviewer"},
		})
	})

	originator := fleet.Agent{
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
// has elapsed, but while the first run is still executing, must be suppressed.
// Before the fix, TryClaimForDispatch only checked the TTL entry; once the
// entry expired the second event could claim the slot and start a concurrent
// duplicate run.
func TestFanOutDedupSurvivesTTLExpiry(t *testing.T) {
	t.Parallel()

	// Use an engine with a 1-second dedup window so the TTL expires quickly.
	e, runner, _ := newTestEngineWithDedup(t, func(c *config.Config) {
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

// TestAgentsRunDeduplicatesDuplicateRequests is a regression test for the
// fleet-wide dedup gap on the HTTP /agents/run path. Before the fix, two
// near-simultaneous agents.run events for the same (agent, repo) both passed
// handleDispatchEvent and launched duplicate runs because the function skipped
// dedup unconditionally (valid only for pre-claimed agent.dispatch events).
func TestAgentsRunDeduplicatesDuplicateRequests(t *testing.T) {
	t.Parallel()
	e, runner, _ := newTestEngineWithDedup(t, nil)

	onDemandEvent := func() Event {
		return Event{
			ID:    GenEventID(),
			Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
			Kind:  "agents.run",
			Actor: "human",
			Payload: map[string]any{
				"target_agent": "pr-reviewer",
			},
		}
	}

	// First on-demand request, must run the agent.
	if err := e.HandleEvent(context.Background(), onDemandEvent()); err != nil {
		t.Fatalf("first HandleEvent: %v", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("expected 1 run after first request, got %d", got)
	}

	// Second identical on-demand request within the dedup window, must be suppressed.
	if err := e.HandleEvent(context.Background(), onDemandEvent()); err != nil {
		t.Fatalf("second HandleEvent: %v", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Errorf("expected still 1 run (second suppressed by dedup), got %d", got)
	}
	if stats := e.DispatchStats(); stats.RunsDeduped != 1 {
		t.Errorf("expected RunsDeduped=1, got %d", stats.RunsDeduped)
	}
}

// TestEngineConcurrentReadsAreRaceFree verifies that concurrent
// HandleEvent calls don't race on internal engine state. The pre-cutover
// hot-reload path (UpdateConfigAndRunners) is gone, every event reads
// fresh from SQLite, so the prior cfgMu/runnersMu race tests don't apply.
// Run with -race.
func TestEngineConcurrentReadsAreRaceFree(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, func(c *config.Config) {
		c.Repos[0].Use = append(c.Repos[0].Use, fleet.Binding{
			Agent:  "arch-reviewer",
			Events: []string{"push"},
		})
	})

	pushEvent := func() Event {
		return Event{
			Repo:  RepoRef{FullName: "owner/repo", Enabled: true},
			Kind:  "push",
			Actor: "bot",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	const goroutines = 8
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_ = e.HandleEvent(ctx, pushEvent())
			}
		}()
	}
	wg.Wait()
}

// stubLastRunRecorder captures LastRunRecorder calls so tests can assert that
// only autonomous events trigger the schedule-view callback.
type stubLastRunRecorder struct {
	mu    sync.Mutex
	calls []struct {
		agent, repo, status string
	}
}

func (s *stubLastRunRecorder) RecordLastRun(agent, repo string, _ time.Time, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, struct {
		agent, repo, status string
	}{agent, repo, status})
}

func (s *stubLastRunRecorder) snapshot() []struct{ agent, repo, status string } {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// autonomousEvent builds an Event for a cron-fired autonomous run, matching
// the shape the scheduler pushes onto the queue.
func autonomousEvent(repo, agentName string) Event {
	return Event{
		ID:         GenEventID(),
		Repo:       RepoRef{FullName: repo, Enabled: true},
		Kind:       "cron",
		Actor:      agentName,
		Payload:    map[string]any{"target_agent": agentName},
		EnqueuedAt: time.Now(),
	}
}

// TestHandleEventAutonomousRunsTargetAgent verifies the engine handles an
// "cron" event by resolving the target agent from the payload and
// running it, same shape as agents.run, no binding lookup required (the
// cron's fire-time authority is enough).
func TestHandleEventAutonomousRunsTargetAgent(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(t, nil)

	if err := e.HandleEvent(context.Background(), autonomousEvent("owner/repo", "arch-reviewer")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("runner called %d times, want 1", got)
	}
	if got := runner.lastSystem(); !strings.Contains(got, "Focus on architecture.") {
		t.Errorf("runner system prompt missing skill content: %q", got)
	}
}

// TestHandleEventAutonomousFiresLastRunRecorder verifies that an autonomous
// event triggers the LastRunRecorder hook so the autonomous scheduler's
// lastRuns map (driving the per-binding schedule view in /agents) reflects
// every run that flowed through the engine.
func TestHandleEventAutonomousFiresLastRunRecorder(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, nil)
	rec := &stubLastRunRecorder{}
	e.WithLastRunRecorder(rec)

	if err := e.HandleEvent(context.Background(), autonomousEvent("owner/repo", "arch-reviewer")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("LastRunRecorder calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].agent != "arch-reviewer" || calls[0].repo != "owner/repo" || calls[0].status != "success" {
		t.Errorf("unexpected last-run record: %+v", calls[0])
	}
}

// TestHandleEventNonAutonomousSkipsLastRunRecorder verifies that label/event
// driven runs (webhook path) do not fire the LastRunRecorder hook, only
// autonomous runs update the cron schedule view.
func TestHandleEventNonAutonomousSkipsLastRunRecorder(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, func(c *config.Config) {
		c.Repos[0].Use = []fleet.Binding{
			{Agent: "arch-reviewer", Labels: []string{"ai:review:arch-reviewer"}},
		}
	})
	rec := &stubLastRunRecorder{}
	e.WithLastRunRecorder(rec)

	ev := labelEvent("issues.labeled", "owner/repo", "ai:review:arch-reviewer", 5)
	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("LastRunRecorder fired for non-autonomous event %d times, want 0", got)
	}
}

// TestHandleEventAutonomousReportsErrorStatus verifies a runner failure
// surfaces as status="error" in the LastRunRecorder callback so the schedule
// view distinguishes broken from healthy bindings without a separate fetch.
func TestHandleEventAutonomousReportsErrorStatus(t *testing.T) {
	t.Parallel()
	e, runner := newTestEngine(t, nil)
	runner.runFn = func(ai.Request) error { return errors.New("boom") }
	rec := &stubLastRunRecorder{}
	e.WithLastRunRecorder(rec)

	err := e.HandleEvent(context.Background(), autonomousEvent("owner/repo", "arch-reviewer"))
	if err == nil {
		t.Fatal("expected runner error to propagate")
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0].status != "error" {
		t.Fatalf("expected one error-status record, got %+v", calls)
	}
}
