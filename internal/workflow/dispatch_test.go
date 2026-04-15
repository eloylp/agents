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

// ─── helpers ─────────────────────────────────────────────────────────────────

type fakeQueue struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (q *fakeQueue) PushEvent(_ context.Context, ev Event) error {
	if q.err != nil {
		return q.err
	}
	q.mu.Lock()
	q.events = append(q.events, ev)
	q.mu.Unlock()
	return nil
}

func (q *fakeQueue) popped() []Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Event, len(q.events))
	copy(out, q.events)
	return out
}

func testDispatchCfg() config.DispatchConfig {
	return config.DispatchConfig{
		MaxDepth:           3,
		MaxFanout:          4,
		DedupWindowSeconds: 300,
	}
}

func testAgentMap() map[string]config.AgentDef {
	return map[string]config.AgentDef{
		"coder": {
			Name:          "coder",
			Description:   "Writes code",
			AllowDispatch: true,
			CanDispatch:   []string{"pr-reviewer"},
		},
		"pr-reviewer": {
			Name:          "pr-reviewer",
			Description:   "Reviews PRs",
			AllowDispatch: true,
			CanDispatch:   []string{"coder"},
		},
		"sec-reviewer": {
			Name:          "sec-reviewer",
			Description:   "Reviews security",
			AllowDispatch: false, // opt-out
		},
	}
}

func testDispatcher(q *fakeQueue) *Dispatcher {
	return NewDispatcher(testDispatchCfg(), testAgentMap(), NewDispatchDedupStore(300), q, zerolog.Nop())
}

func originatorAgent(name string) config.AgentDef {
	return testAgentMap()[name]
}

func testEvent(repo string, number int) Event {
	return Event{
		ID:     "root-123",
		Repo:   RepoRef{FullName: repo, Enabled: true},
		Kind:   "issues.labeled",
		Number: number,
	}
}

// ─── Dispatcher tests ─────────────────────────────────────────────────────────

func TestDispatcherEnqueuesValidRequest(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 42, Reason: "please review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 42), "root-123", 0, reqs)

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 enqueued event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != "agent.dispatch" {
		t.Errorf("kind: got %q, want %q", ev.Kind, "agent.dispatch")
	}
	if ev.Payload["target_agent"] != "pr-reviewer" {
		t.Errorf("target_agent: got %v", ev.Payload["target_agent"])
	}
	if ev.Payload["dispatch_depth"] != 1 {
		t.Errorf("dispatch_depth: got %v", ev.Payload["dispatch_depth"])
	}
	if ev.Payload["root_event_id"] != "root-123" {
		t.Errorf("root_event_id: got %v", ev.Payload["root_event_id"])
	}

	stats := d.Stats()
	if stats.RequestedTotal != 1 {
		t.Errorf("requested_total: got %d, want 1", stats.RequestedTotal)
	}
	if stats.Enqueued != 1 {
		t.Errorf("enqueued: got %d, want 1", stats.Enqueued)
	}
}

func TestDispatcherDropsSelfDispatch(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// coder trying to dispatch itself
	reqs := []ai.DispatchRequest{{Agent: "coder", Number: 1, Reason: "self"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	if len(q.popped()) != 0 {
		t.Errorf("self-dispatch should be dropped, got %d events", len(q.popped()))
	}
	if d.Stats().DroppedSelf != 1 {
		t.Errorf("dropped_self: got %d, want 1", d.Stats().DroppedSelf)
	}
}

func TestDispatcherDropsNotInWhitelist(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// coder is NOT in can_dispatch of sec-reviewer; coder cannot dispatch sec-reviewer
	reqs := []ai.DispatchRequest{{Agent: "sec-reviewer", Number: 1, Reason: "check security"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	if len(q.popped()) != 0 {
		t.Errorf("not-in-whitelist should be dropped")
	}
	if d.Stats().DroppedNoWhitelist != 1 {
		t.Errorf("dropped_no_whitelist: got %d, want 1", d.Stats().DroppedNoWhitelist)
	}
}

func TestDispatcherDropsNoOptin(t *testing.T) {
	t.Parallel()
	// Make a custom agent map where pr-reviewer has allow_dispatch: false
	agents := testAgentMap()
	prReviewer := agents["pr-reviewer"]
	prReviewer.AllowDispatch = false
	agents["pr-reviewer"] = prReviewer

	q := &fakeQueue{}
	d := NewDispatcher(testDispatchCfg(), agents, NewDispatchDedupStore(300), q, zerolog.Nop())

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	if len(q.popped()) != 0 {
		t.Errorf("no-optin should be dropped")
	}
	if d.Stats().DroppedNoOptin != 1 {
		t.Errorf("dropped_no_optin: got %d, want 1", d.Stats().DroppedNoOptin)
	}
}

func TestDispatcherDropsExceedsMaxDepth(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	cfg := testDispatchCfg()
	cfg.MaxDepth = 2
	d := NewDispatcher(cfg, testAgentMap(), NewDispatchDedupStore(300), q, zerolog.Nop())

	// currentDepth=2, newDepth=3 > MaxDepth=2 → drop
	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 2, reqs)

	if len(q.popped()) != 0 {
		t.Errorf("exceeds-max-depth should be dropped")
	}
	if d.Stats().DroppedDepth != 1 {
		t.Errorf("dropped_depth: got %d, want 1", d.Stats().DroppedDepth)
	}
}

func TestDispatcherDropsExceedsMaxFanout(t *testing.T) {
	t.Parallel()

	// Build an agent map with many valid targets.
	agents := map[string]config.AgentDef{
		"coder": {Name: "coder", Description: "Codes", CanDispatch: []string{"a", "b", "c", "d", "e"}},
		"a":     {Name: "a", Description: "Agent A", AllowDispatch: true},
		"b":     {Name: "b", Description: "Agent B", AllowDispatch: true},
		"c":     {Name: "c", Description: "Agent C", AllowDispatch: true},
		"d":     {Name: "d", Description: "Agent D", AllowDispatch: true},
		"e":     {Name: "e", Description: "Agent E", AllowDispatch: true},
	}
	cfg := testDispatchCfg()
	cfg.MaxFanout = 3
	q := &fakeQueue{}
	d := NewDispatcher(cfg, agents, NewDispatchDedupStore(300), q, zerolog.Nop())

	reqs := []ai.DispatchRequest{
		{Agent: "a", Number: 1, Reason: "r"},
		{Agent: "b", Number: 2, Reason: "r"},
		{Agent: "c", Number: 3, Reason: "r"},
		{Agent: "d", Number: 4, Reason: "r"}, // exceeds fanout
		{Agent: "e", Number: 5, Reason: "r"}, // exceeds fanout
	}
	originator := config.AgentDef{Name: "coder", CanDispatch: []string{"a", "b", "c", "d", "e"}}
	d.ProcessDispatches(context.Background(), originator, testEvent("owner/repo", 0), "root-1", 0, reqs)

	if got := len(q.popped()); got != 3 {
		t.Errorf("expected 3 enqueued (fanout cap), got %d", got)
	}
	if d.Stats().DroppedFanout != 2 {
		t.Errorf("dropped_fanout: got %d, want 2", d.Stats().DroppedFanout)
	}
}

func TestDispatcherDeduplicatesWithinWindow(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 42, Reason: "review"}}
	ev := testEvent("owner/repo", 42)

	// First dispatch: should be enqueued.
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, reqs)
	if len(q.popped()) != 1 {
		t.Fatalf("first dispatch should be enqueued")
	}

	// Second dispatch with same (target, repo, number): should be deduped.
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, reqs)
	if len(q.popped()) != 1 {
		t.Errorf("second dispatch should be deduped; got %d events total", len(q.popped()))
	}
	if d.Stats().Deduped != 1 {
		t.Errorf("deduped: got %d, want 1", d.Stats().Deduped)
	}
}

// ─── DispatchDedupStore tests ─────────────────────────────────────────────────

func TestDispatchDedupStoreSeenOrAdd(t *testing.T) {
	t.Parallel()
	s := NewDispatchDedupStore(300)
	now := time.Now()

	// First call: not seen → false.
	if s.SeenOrAdd("target", "owner/repo", 42, now) {
		t.Error("first call should return false (not seen)")
	}
	// Second call within window: seen → true.
	if !s.SeenOrAdd("target", "owner/repo", 42, now.Add(time.Second)) {
		t.Error("second call within window should return true (seen)")
	}
	// Different number: different key → not seen.
	if s.SeenOrAdd("target", "owner/repo", 99, now) {
		t.Error("different number should not be seen")
	}
}

func TestDispatchDedupStoreExpiry(t *testing.T) {
	t.Parallel()
	s := NewDispatchDedupStore(1) // 1 second TTL
	now := time.Now()

	s.SeenOrAdd("target", "repo", 1, now)

	// After TTL: expired → not seen.
	if s.SeenOrAdd("target", "repo", 1, now.Add(2*time.Second)) {
		t.Error("entry should be expired after TTL")
	}
}

func TestDispatchDedupStoreBackgroundEviction(t *testing.T) {
	t.Parallel()
	s := NewDispatchDedupStore(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	now := time.Now()
	s.SeenOrAdd("x", "repo", 1, now)

	// Wait for background eviction.
	time.Sleep(1500 * time.Millisecond)

	// After eviction, entry should be gone (not seen).
	if s.SeenOrAdd("x", "repo", 1, time.Now()) {
		t.Error("entry should have been evicted by background sweeper")
	}
}

// ─── Engine dispatch handling tests ──────────────────────────────────────────

func TestEngineHandlesAgentDispatchEvent(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{}
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
		ID:   "root-abc",
		Repo: RepoRef{FullName: "owner/repo", Enabled: true},
		Kind: "agent.dispatch",
		Number: 5,
		Actor: "coder",
		Payload: map[string]any{
			"target_agent":   "pr-reviewer",
			"reason":         "please review this PR",
			"root_event_id":  "root-abc",
			"dispatch_depth": 1,
			"invoked_by":     "coder",
		},
	}

	if err := e.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if runner.callCount() != 1 {
		t.Errorf("expected 1 run for dispatched agent, got %d", runner.callCount())
	}
}

func TestEngineDispatchEventUnboundTargetReturnsError(t *testing.T) {
	t.Parallel()
	runner := &stubRunner{}
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Processor: config.ProcessorConfig{
				MaxConcurrentAgents: 4,
				Dispatch:            config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300},
			},
			AIBackends: map[string]config.AIBackendConfig{"claude": {Command: "claude"}},
		},
		Skills: map[string]config.SkillDef{},
		Agents: []config.AgentDef{
			{Name: "coder", Backend: "claude", Prompt: "Code."},
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []config.Binding{{Agent: "coder", Labels: []string{"ai:code"}}},
				// pr-reviewer NOT bound to this repo
			},
		},
	}
	e := NewEngine(cfg, map[string]ai.Runner{"claude": runner}, nil, zerolog.Nop())

	ev := Event{
		Repo: RepoRef{FullName: "owner/repo", Enabled: true},
		Kind: "agent.dispatch",
		Payload: map[string]any{
			"target_agent":   "pr-reviewer",
			"reason":         "review",
			"dispatch_depth": 1,
		},
	}
	err := e.HandleEvent(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("expected 'not bound' error, got %v", err)
	}
}

func TestDispatcherNormalizesAgentName(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// Mixed-case and whitespace in request — should be normalized.
	reqs := []ai.DispatchRequest{{Agent: "  PR-Reviewer  ", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected normalized name to match; got %d events", len(events))
	}
}

func TestDispatcherCountersAccumulateAcrossMultipleCalls(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// Two valid dispatches.
	for range 2 {
		reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "r"}}
		// Use a unique number each time to avoid dedup.
		d.dedup = NewDispatchDedupStore(300) // reset dedup for clean test
		d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-x", 0, reqs)
	}

	stats := d.Stats()
	if stats.RequestedTotal != 2 {
		t.Errorf("requested_total: got %d, want 2", stats.RequestedTotal)
	}
	if stats.Enqueued != 2 {
		t.Errorf("enqueued: got %d, want 2", stats.Enqueued)
	}
}

func TestDispatcherHandlesQueueError(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{err: errors.New("queue full")}
	d := testDispatcher(q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	// Should not panic or propagate error — it logs and continues.
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	// Enqueued should not have incremented since queue failed.
	if d.Stats().Enqueued != 0 {
		t.Errorf("enqueued should be 0 on queue error, got %d", d.Stats().Enqueued)
	}
}

func TestDispatcherOmittedNumberFallsBackToEventNumber(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// req.Number == 0 (agent omitted number field) — must fall back to ev.Number.
	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 0, Reason: "review"}}
	ev := testEvent("owner/repo", 42)
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, reqs)

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 enqueued event, got %d", len(events))
	}
	if events[0].Number != 42 {
		t.Errorf("dispatch event number: got %d, want 42 (fallback from ev.Number)", events[0].Number)
	}
}

func TestDispatcherDedupUsesEventNumberWhenRequestNumberOmitted(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// Two dispatch requests from different event numbers, both with omitted req.Number.
	// They must NOT collapse into the same dedup key.
	req := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 0, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 10), "root-1", 0, req)
	d.dedup = NewDispatchDedupStore(300) // reset dedup to test second dispatch independently
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 20), "root-2", 0, req)

	events := q.popped()
	if len(events) != 2 {
		t.Fatalf("expected 2 enqueued events (distinct numbers), got %d", len(events))
	}
	if events[0].Number != 10 {
		t.Errorf("first event number: got %d, want 10", events[0].Number)
	}
	if events[1].Number != 20 {
		t.Errorf("second event number: got %d, want 20", events[1].Number)
	}
}

func TestDispatcherDedupeRolledBackOnEnqueueFailure(t *testing.T) {
	t.Parallel()
	// First call uses a failing queue — enqueue fails, so the dedup entry must
	// be rolled back.
	qFail := &fakeQueue{err: errors.New("queue full")}
	d := testDispatcher(qFail)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, reqs)

	// Now swap in a healthy queue and retry the same dispatch.
	qOK := &fakeQueue{}
	d.queue = qOK
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-2", 0, reqs)

	events := qOK.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 enqueued event after retry, got %d (dedup was not rolled back)", len(events))
	}
}

// TestCheckAndMarkAutonomousRunSuppressesNearSimultaneousDispatch is a regression
// test for the cron-first dedup ordering: when an autonomous run starts first and
// claims the dedup slot, a near-simultaneous dispatch targeting the same agent/repo
// must be suppressed until ClearAutonomousRunMark is called.
func TestCheckAndMarkAutonomousRunSuppressesNearSimultaneousDispatch(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(q)

	// Simulate: cron run starts and claims the dedup slot.
	alreadySeen := d.CheckAndMarkAutonomousRun("coder", "owner/repo", time.Now())
	if alreadySeen {
		t.Fatal("CheckAndMarkAutonomousRun: expected false on first call (no prior dispatch)")
	}

	// While the cron run is in-flight, a dispatch targeting the same agent/repo
	// must be suppressed (coder dispatching to itself is not allowed, so use
	// pr-reviewer as the originator dispatching to coder).
	originator := originatorAgent("pr-reviewer")
	ev := testEvent("owner/repo", 0)
	d.ProcessDispatches(context.Background(), originator, ev, "root-x", 0, []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch while cron holds slot"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron run already holds the dedup slot")
	}

	// Cron run ends — clear the mark.
	d.ClearAutonomousRunMark("coder", "owner/repo")

	// After the mark is cleared a new dispatch to the same target must succeed.
	d.ProcessDispatches(context.Background(), originator, ev, "root-y", 0, []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch after cron released slot"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued after ClearAutonomousRunMark")
	}
}

func TestDispatchDedupStoreStartSmallTTLDoesNotPanic(t *testing.T) {
	t.Parallel()
	// TTL values of 1, 2, 3 seconds previously could produce a very small
	// ticker interval; ensure Start does not panic for these values.
	for _, ttl := range []int{1, 2, 3} {
		s := NewDispatchDedupStore(ttl)
		ctx, cancel := context.WithCancel(context.Background())
		s.Start(ctx) // must not panic
		cancel()
	}
}
