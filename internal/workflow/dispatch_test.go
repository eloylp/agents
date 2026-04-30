package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
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

func testAgentMap() map[string]fleet.Agent {
	return map[string]fleet.Agent{
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

// testDispatcher seeds testAgentMap into a tempdir SQLite and returns a
// Dispatcher reading from it. Agents are written individually with a
// minimal claude backend so the FK constraints pass.
func testDispatcher(t *testing.T, q *fakeQueue) *Dispatcher {
	t.Helper()
	st := dispatchTestStore(t)
	return NewDispatcher(testDispatchCfg(), st, NewDispatchDedupStore(300), q, zerolog.Nop())
}

// dispatchTestStore seeds the testAgentMap fleet into a tempdir SQLite
// and returns the store wrapping it.
func dispatchTestStore(t *testing.T) *store.Store {
	t.Helper()
	return seedAgentMap(t, testAgentMap())
}

// seedAgentMap seeds an arbitrary agent map into a fresh tempdir SQLite,
// filling in Backend / Prompt where missing so the store's validators
// pass. Returns the live store.
func seedAgentMap(t *testing.T, m map[string]fleet.Agent) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	agents := []fleet.Agent{}
	for _, a := range m {
		if a.Backend == "" {
			a.Backend = "claude"
		}
		if a.Prompt == "" {
			a.Prompt = "test"
		}
		// Description is required for any agent that appears in another
		// agent's CanDispatch list. seedAgentMap is permissive: fill in a
		// default so the validator doesn't reject the seed.
		if a.AllowDispatch && a.Description == "" {
			a.Description = "test"
		}
		agents = append(agents, a)
	}
	if err := st.ImportAll(agents, nil, map[string]fleet.Skill{}, map[string]fleet.Backend{"claude": {Command: "claude"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st
}

func originatorAgent(name string) fleet.Agent {
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
	d := testDispatcher(t, q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 42, Reason: "please review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 42), "root-123", 0, "span-parent-42", reqs)

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
	// parent_span_id must be threaded through so child runs can link spans.
	if ev.Payload["parent_span_id"] != "span-parent-42" {
		t.Errorf("parent_span_id: got %v, want %q", ev.Payload["parent_span_id"], "span-parent-42")
	}
	// The synthetic event must carry its own unique ID, not the root correlation ID.
	if ev.ID == "" {
		t.Error("dispatch event ID must not be empty")
	}
	if ev.ID == "root-123" {
		t.Error("dispatch event ID must differ from root_event_id")
	}

	stats := d.Stats()
	if stats.RequestedTotal != 1 {
		t.Errorf("requested_total: got %d, want 1", stats.RequestedTotal)
	}
	if stats.Enqueued != 1 {
		t.Errorf("enqueued: got %d, want 1", stats.Enqueued)
	}
}

func TestDispatcherEventIDIsUniquePerHop(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	// Dispatch the same agent against two different issue numbers from the same
	// root event. Each synthetic event must carry a distinct ID that is not
	// equal to rootEventID. (dedup keys differ by number so both are enqueued)
	reqs := []ai.DispatchRequest{
		{Agent: "pr-reviewer", Number: 1, Reason: "review pr 1"},
		{Agent: "pr-reviewer", Number: 2, Reason: "review pr 2"},
	}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-xyz", 0, "", reqs)

	events := q.popped()
	if len(events) != 2 {
		t.Fatalf("expected 2 enqueued events, got %d", len(events))
	}
	if events[0].ID == "root-xyz" || events[1].ID == "root-xyz" {
		t.Error("dispatch event IDs must not equal rootEventID")
	}
	if events[0].ID == events[1].ID {
		t.Error("consecutive dispatch events must have distinct IDs")
	}
}

func TestDispatcherDropsRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		targetAgent  string
		currentDepth int
		modifyCfg    func(*config.DispatchConfig)
		modifyAgents func(map[string]fleet.Agent)
		wantStat     func(DispatchStats) int64
		wantStatName string
	}{
		{
			name:         "self-dispatch",
			targetAgent:  "coder",
			wantStat:     func(s DispatchStats) int64 { return s.DroppedSelf },
			wantStatName: "dropped_self",
		},
		{
			name:         "target not in can_dispatch whitelist",
			targetAgent:  "sec-reviewer",
			wantStat:     func(s DispatchStats) int64 { return s.DroppedNoWhitelist },
			wantStatName: "dropped_no_whitelist",
		},
		{
			name:        "target has allow_dispatch false",
			targetAgent: "pr-reviewer",
			modifyAgents: func(agents map[string]fleet.Agent) {
				a := agents["pr-reviewer"]
				a.AllowDispatch = false
				agents["pr-reviewer"] = a
			},
			wantStat:     func(s DispatchStats) int64 { return s.DroppedNoOptin },
			wantStatName: "dropped_no_optin",
		},
		{
			name:         "exceeds max depth",
			targetAgent:  "pr-reviewer",
			currentDepth: 2,
			modifyCfg:    func(cfg *config.DispatchConfig) { cfg.MaxDepth = 2 },
			wantStat:     func(s DispatchStats) int64 { return s.DroppedDepth },
			wantStatName: "dropped_depth",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeQueue{}
			cfg := testDispatchCfg()
			if tc.modifyCfg != nil {
				tc.modifyCfg(&cfg)
			}
			agents := testAgentMap()
			if tc.modifyAgents != nil {
				tc.modifyAgents(agents)
			}
			d := NewDispatcher(cfg, seedAgentMap(t, agents), NewDispatchDedupStore(300), q, zerolog.Nop())
			reqs := []ai.DispatchRequest{{Agent: tc.targetAgent, Number: 1, Reason: "test"}}
			d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", tc.currentDepth, "", reqs)
			if len(q.popped()) != 0 {
				t.Errorf("expected dispatch to be dropped, got %d events", len(q.popped()))
			}
			if got := tc.wantStat(d.Stats()); got != 1 {
				t.Errorf("%s: got %d, want 1", tc.wantStatName, got)
			}
		})
	}
}

func TestDispatcherDropsExceedsMaxFanout(t *testing.T) {
	t.Parallel()

	// Build an agent map with many valid targets.
	agents := map[string]fleet.Agent{
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
	d := NewDispatcher(cfg, seedAgentMap(t, agents), NewDispatchDedupStore(300), q, zerolog.Nop())

	reqs := []ai.DispatchRequest{
		{Agent: "a", Number: 1, Reason: "r"},
		{Agent: "b", Number: 2, Reason: "r"},
		{Agent: "c", Number: 3, Reason: "r"},
		{Agent: "d", Number: 4, Reason: "r"}, // exceeds fanout
		{Agent: "e", Number: 5, Reason: "r"}, // exceeds fanout
	}
	originator := fleet.Agent{Name: "coder", CanDispatch: []string{"a", "b", "c", "d", "e"}}
	d.ProcessDispatches(context.Background(), originator, testEvent("owner/repo", 0), "root-1", 0, "", reqs)

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
	d := testDispatcher(t, q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 42, Reason: "review"}}
	ev := testEvent("owner/repo", 42)

	// First dispatch: should be enqueued.
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, "", reqs)
	if len(q.popped()) != 1 {
		t.Fatalf("first dispatch should be enqueued")
	}

	// Second dispatch with same (target, repo, number): should be deduped.
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, "", reqs)
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
	go func() { _ = s.Run(ctx) }()

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
			AIBackends: map[string]fleet.Backend{"claude": {Command: "claude"}},
		},
		Skills: map[string]fleet.Skill{},
		Agents: []fleet.Agent{
			{Name: "coder", Backend: "claude", Prompt: "Code."},
			{Name: "pr-reviewer", Backend: "claude", Prompt: "Review."},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "coder", Labels: []string{"ai:code"}}},
				// pr-reviewer NOT bound to this repo
			},
		},
	}
	e := newEngineFromCfg(t, cfg, map[string]ai.Runner{"claude": runner}, nil)

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
	d := testDispatcher(t, q)

	// Mixed-case and whitespace in request — should be normalized.
	reqs := []ai.DispatchRequest{{Agent: "  PR-Reviewer  ", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, "", reqs)

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected normalized name to match; got %d events", len(events))
	}
}

func TestDispatcherCountersAccumulateAcrossMultipleCalls(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	// Two valid dispatches.
	for range 2 {
		reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "r"}}
		// Use a unique number each time to avoid dedup.
		d.dedup = NewDispatchDedupStore(300) // reset dedup for clean test
		d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-x", 0, "", reqs)
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
	d := testDispatcher(t, q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	err := d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, "", reqs)

	if err == nil {
		t.Fatal("expected error from enqueue failure, got nil")
	}

	// Enqueued should not have incremented since queue failed.
	if d.Stats().Enqueued != 0 {
		t.Errorf("enqueued should be 0 on queue error, got %d", d.Stats().Enqueued)
	}
}

func TestDispatcherOmittedNumberFallsBackToEventNumber(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	// req.Number == 0 (agent omitted number field) — must fall back to ev.Number.
	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 0, Reason: "review"}}
	ev := testEvent("owner/repo", 42)
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-1", 0, "", reqs)

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
	d := testDispatcher(t, q)

	// Two dispatch requests from different event numbers, both with omitted req.Number.
	// They must NOT collapse into the same dedup key.
	req := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 0, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 10), "root-1", 0, "", req)
	d.dedup = NewDispatchDedupStore(300) // reset dedup to test second dispatch independently
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 20), "root-2", 0, "", req)

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

func TestDispatcherRetrySucceedsAfterEnqueueFailure(t *testing.T) {
	t.Parallel()
	// First call uses a failing queue — enqueue fails. Because the dedup slot is
	// claimed only after a successful enqueue, no dedup entry is written and
	// the retry is not suppressed.
	qFail := &fakeQueue{err: errors.New("queue full")}
	d := testDispatcher(t, qFail)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 1, Reason: "review"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-1", 0, "", reqs)

	// Now swap in a healthy queue and retry the same dispatch.
	qOK := &fakeQueue{}
	d.queue = qOK
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 1), "root-2", 0, "", reqs)

	events := qOK.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 enqueued event after retry, got %d", len(events))
	}
}

// TestDispatchClaimOnlyVisibleAfterSuccessfulEnqueue is a regression test for
// the lost-work race: if ProcessDispatches fails to enqueue a dispatch event,
// DispatchAlreadyClaimed must return false so the autonomous scheduler can
// still run the target. Previously, SeenOrAdd was called before PushEvent and
// a failed enqueue followed by a dedup rollback still left a brief window where
// DispatchAlreadyClaimed could return true, causing both paths to skip.
func TestDispatchClaimOnlyVisibleAfterSuccessfulEnqueue(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{err: errors.New("queue full")}
	d := testDispatcher(t, q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 0, Reason: "check"}}
	d.ProcessDispatches(context.Background(), originatorAgent("coder"), testEvent("owner/repo", 0), "root-1", 0, "", reqs)

	// The enqueue failed, so the dispatch slot must NOT be claimed.
	if d.DispatchAlreadyClaimed("pr-reviewer", "owner/repo", time.Now()) {
		t.Error("DispatchAlreadyClaimed returned true after a failed enqueue: phantom claim left by failed dispatch")
	}
}

// TestMarkAutonomousRunSuppressesNearSimultaneousDispatch is a regression
// test for the cron-first dedup ordering: when an autonomous run starts and
// MarkAutonomousRun writes a cron-namespace mark, dispatches targeting the
// same agent/repo with number=0 (autonomous context) must be suppressed for
// the full dedup_window_seconds — both while the run is in-flight and after
// it completes.
func TestMarkAutonomousRunSuppressesNearSimultaneousDispatch(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	// Simulate: cron run confirms it will proceed and writes the cron-namespace mark.
	alreadyClaimed := d.DispatchAlreadyClaimed("coder", "owner/repo", time.Now())
	if alreadyClaimed {
		t.Fatal("DispatchAlreadyClaimed: expected false on first call (no prior dispatch)")
	}
	d.MarkAutonomousRun("coder", "owner/repo", time.Now())

	// Dispatches targeting the same agent/repo with number=0 must be suppressed
	// for the full dedup window (coder dispatching to itself is not allowed, so use
	// pr-reviewer as the originator dispatching to coder).
	originator := originatorAgent("pr-reviewer")
	ev := testEvent("owner/repo", 0)
	d.ProcessDispatches(context.Background(), originator, ev, "root-x", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch while cron mark is active"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron mark is active within dedup window")
	}

	// The mark persists — a second dispatch attempt within the same window is also suppressed.
	d.ProcessDispatches(context.Background(), originator, ev, "root-y", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "second dispatch attempt within dedup window"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected second dispatch suppressed: cron mark still active within dedup window")
	}
}

// TestMarkAutonomousRunDoesNotSuppressDispatchForDifferentNumber verifies that
// a cron mark (number=0, autonomous context) does not suppress dispatches
// targeting a different item number on the same repo. This guards against the
// cron mark being too broad and blocking valid event-driven dispatches such as
// "coder → pr-reviewer for PR #42" just because pr-reviewer had a cron run.
func TestMarkAutonomousRunDoesNotSuppressDispatchForDifferentNumber(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	// Cron run for pr-reviewer marks (pr-reviewer, owner/repo, 0).
	d.MarkAutonomousRun("coder", "owner/repo", time.Now())

	// A dispatch targeting coder for PR #42 (number=42) must still be enqueued
	// — it is a different item context from the autonomous cron run (number=0).
	originator := originatorAgent("pr-reviewer")
	ev := testEvent("owner/repo", 42)
	d.ProcessDispatches(context.Background(), originator, ev, "root-pr42", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Number: 42, Reason: "review this specific PR"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued: cron mark (number=0) must not suppress dispatch for PR #42 (number=42)")
	}
}

// TestPostRunDispatchSuppressedWithinDedupWindow verifies that dispatches arriving
// AFTER an autonomous run completes are still blocked for the full dedup_window_seconds.
// This is the key difference from the prior implementation, which cleared the mark
// on run completion and allowed post-run dispatches to slip through.
func TestPostRunDispatchSuppressedWithinDedupWindow(t *testing.T) {
	t.Parallel()

	// Use a short TTL (2 seconds) so we can verify expiry without long sleeps.
	dedup := NewDispatchDedupStore(2)
	agents := map[string]fleet.Agent{
		"pr-reviewer": {Name: "pr-reviewer", AllowDispatch: true, CanDispatch: []string{"coder"}},
		"coder":        {Name: "coder", AllowDispatch: true},
	}
	cfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 2}
	q := &fakeQueue{}
	d := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q, zerolog.Nop())

	// Autonomous run confirms it will proceed and writes the cron mark.
	now := time.Now()
	if d.DispatchAlreadyClaimed("coder", "owner/repo", now) {
		t.Fatal("DispatchAlreadyClaimed: expected false on first call (no prior dispatch)")
	}
	d.MarkAutonomousRun("coder", "owner/repo", now)

	// Dispatch arriving after the run — still within the TTL window — must be suppressed.
	originator := agents["pr-reviewer"]
	ev := Event{Repo: RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "cron", Number: 0}
	d.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch after run, within window"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron mark still active within dedup window")
	}

	// After the TTL expires the dispatch must succeed. The expiry is simulated
	// by creating a fresh dedup store (no cron mark) rather than advancing a
	// real clock.
	d2 := NewDispatcher(cfg, seedAgentMap(t, agents), NewDispatchDedupStore(2), q, zerolog.Nop())
	d2.ProcessDispatches(context.Background(), originator, ev, "root-2", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch after window expired"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued: dedup window has expired")
	}
}

func TestDispatchDedupStoreStartSmallTTLDoesNotPanic(t *testing.T) {
	t.Parallel()
	// TTL values of 1, 2, 3 seconds previously could produce a very
	// small ticker interval; ensure Run does not panic for these values.
	for _, ttl := range []int{1, 2, 3} {
		s := NewDispatchDedupStore(ttl)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = s.Run(ctx); close(done) }()
		cancel()
		<-done
	}
}

// TestRemoveCronMarkWithOverlappingRunsRefcount verifies that a rollback from one
// failed autonomous run does not clear a cron mark that a second overlapping run
// is still relying on. Without refcount semantics, the first rollback would delete
// the shared key and allow dispatches to slip through while the second run is
// still in flight.
// TestConcurrentDispatchesDoNotDuplicateEnqueue is a regression test for the
// non-atomic Seen+PushEvent+SeenOrAdd race: two concurrent ProcessDispatches
// calls for the same (target, repo, number) must enqueue exactly one event, not
// two. The two-phase TryClaim/CommitClaim scheme ensures that the first caller
// wins the pending reservation and the second is blocked at TryClaim before it
// even attempts to enqueue.
func TestConcurrentDispatchesDoNotDuplicateEnqueue(t *testing.T) {
	t.Parallel()

	q := &fakeQueue{}
	d := testDispatcher(t, q)

	reqs := []ai.DispatchRequest{{Agent: "pr-reviewer", Number: 42, Reason: "review"}}
	ev := testEvent("owner/repo", 42)

	var wg sync.WaitGroup
	const goroutines = 20
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			d.ProcessDispatches(context.Background(), originatorAgent("coder"), ev, "root-concurrent", 0, "", reqs)
		}()
	}
	wg.Wait()

	events := q.popped()
	if len(events) != 1 {
		t.Errorf("concurrent dispatches: expected exactly 1 enqueued event, got %d", len(events))
	}
	if d.Stats().Enqueued != 1 {
		t.Errorf("enqueued counter: got %d, want 1", d.Stats().Enqueued)
	}
}

func TestRemoveCronMarkWithOverlappingRunsRefcount(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	d := testDispatcher(t, q)

	now := time.Now()
	// Simulate two overlapping autonomous runs for the same (agent, repo).
	d.MarkAutonomousRun("coder", "owner/repo", now)
	d.MarkAutonomousRun("coder", "owner/repo", now)

	// First run fails: rolls back. The second run is still in flight, so
	// dispatches must still be suppressed.
	d.RollbackAutonomousRun("coder", "owner/repo")

	originator := originatorAgent("pr-reviewer")
	ev := testEvent("owner/repo", 0)
	d.ProcessDispatches(context.Background(), originator, ev, "root-overlap", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch while second run is still in flight"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch suppressed: second in-flight run's cron mark must survive the first run's rollback")
	}

	// Second run also fails: mark is now fully released.
	d.RollbackAutonomousRun("coder", "owner/repo")

	// A new dispatcher with a fresh store simulates the expired/cleared state.
	q2 := &fakeQueue{}
	d2 := testDispatcher(t, q2)
	d2.ProcessDispatches(context.Background(), originator, ev, "root-after", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch after both runs rolled back"},
	})
	if len(q2.popped()) != 1 {
		t.Error("expected dispatch enqueued: all cron marks cleared after both rollbacks")
	}
}

// TestLongRunningCronMarkBlocksDispatchPastTTL is a regression test for the
// case where an autonomous run outlasts its dedup_window_seconds TTL. Without
// the refcount-based guard, the sweeper would evict the cron entry once its
// expiresAt passed, and TryClaimForDispatch would no longer see the mark —
// allowing a dispatch to race the still-in-flight cron run.
func TestLongRunningCronMarkBlocksDispatchPastTTL(t *testing.T) {
	t.Parallel()

	// 1-second TTL so we can simulate expiry without real sleeps.
	const ttlSeconds = 1
	dedup := NewDispatchDedupStore(ttlSeconds)
	agents := map[string]fleet.Agent{
		"pr-reviewer": {Name: "pr-reviewer", AllowDispatch: true, CanDispatch: []string{"coder"}},
		"coder":        {Name: "coder", AllowDispatch: true},
	}
	cfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: ttlSeconds}
	q := &fakeQueue{}
	d := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q, zerolog.Nop())

	// Autonomous run starts; writes cron mark.
	start := time.Now()
	d.MarkAutonomousRun("coder", "owner/repo", start)

	// Simulate the sweeper running after the TTL has elapsed but before the
	// run completes. The cron mark's refcount is still 1, so it must NOT be
	// evicted.
	pastTTL := start.Add(2 * time.Duration(ttlSeconds) * time.Second)
	dedup.evict(pastTTL)

	// A dispatch arriving after the TTL window must still be suppressed because
	// the cron run is still in flight (refcount > 0).
	originator := agents["pr-reviewer"]
	ev := Event{Repo: RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "cron", Number: 0}
	d.ProcessDispatches(context.Background(), originator, ev, "root-past-ttl", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch while long-running cron job is still in flight"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron run still in flight even though TTL has elapsed")
	}

	// Once the run completes successfully, the refcount is decremented via
	// FinalizeAutonomousRun. The entry itself is kept until the TTL expires —
	// but since we advanced past the TTL in the simulation, the next eviction
	// pass will remove it and dispatches may then proceed.
	d.FinalizeAutonomousRun("coder", "owner/repo")
	dedup.evict(pastTTL) // TTL already elapsed; now that refcount is 0, entry is evicted.
	q2 := &fakeQueue{}
	d2 := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q2, zerolog.Nop())
	d2.ProcessDispatches(context.Background(), originator, ev, "root-after-run", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch after run completed and TTL expired"},
	})
	if len(q2.popped()) != 1 {
		t.Error("expected dispatch enqueued: cron entry evicted after finalize + TTL expiry")
	}
}

// TestFinalizeAutonomousRunKeepsTTLBlocksDispatchWithinWindow verifies that
// FinalizeAutonomousRun (success path) preserves the cron entry so that
// dispatches targeting the same (agent, repo, 0) slot are still suppressed
// within the dedup window, but a second cron run is never blocked.
func TestFinalizeAutonomousRunKeepsTTLBlocksDispatchWithinWindow(t *testing.T) {
	t.Parallel()

	const ttlSeconds = 60
	dedup := NewDispatchDedupStore(ttlSeconds)
	agents := map[string]fleet.Agent{
		"pr-reviewer": {Name: "pr-reviewer", AllowDispatch: true, CanDispatch: []string{"coder"}},
		"coder":       {Name: "coder", AllowDispatch: true},
	}
	cfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: ttlSeconds}
	originator := agents["pr-reviewer"]
	ev := Event{Repo: RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "cron", Number: 0}

	q1 := &fakeQueue{}
	d1 := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q1, zerolog.Nop())

	// Cron run starts and completes successfully.
	now := time.Now()
	d1.TryMarkAutonomousRun("coder", "owner/repo", now)
	d1.FinalizeAutonomousRun("coder", "owner/repo")

	// Within the TTL window, a dispatch to the same (agent, repo, 0) slot must
	// still be suppressed — the entry is kept for the full dedup window.
	q2 := &fakeQueue{}
	d2 := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q2, zerolog.Nop())
	d2.ProcessDispatches(context.Background(), originator, ev, "root-post-run", 0, "", []ai.DispatchRequest{
		{Agent: "coder", Reason: "dispatch shortly after successful cron run"},
	})
	if len(q2.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron run entry still within TTL window")
	}

	// A second cron run within the window must NOT be suppressed: cron runs
	// check the dispatch namespace (not the cron namespace), so the mark never
	// blocks repeated autonomous runs.
	q3 := &fakeQueue{}
	d3 := NewDispatcher(cfg, seedAgentMap(t, agents), dedup, q3, zerolog.Nop())
	if !d3.TryMarkAutonomousRun("coder", "owner/repo", now) {
		t.Error("expected second cron run to proceed: cron marks must not suppress other cron runs")
	}
	d3.FinalizeAutonomousRun("coder", "owner/repo")
}

// TestSuccessfulCronRunRefcountIsZeroAfterFinalize verifies that
// FinalizeCronMark brings cronRefCounts to zero after a successful run,
// allowing evict() to clean up the entry once its TTL has passed.
func TestSuccessfulCronRunRefcountIsZeroAfterFinalize(t *testing.T) {
	t.Parallel()

	const ttlSeconds = 1
	dedup := NewDispatchDedupStore(ttlSeconds)
	now := time.Now()

	// Mark a cron run and immediately finalize it (simulating a successful run).
	dedup.TryClaimForCron("coder", "owner/repo", 0, now)
	dedup.FinalizeCronMark("coder", "owner/repo", 0)

	// Evict with a time past the TTL; the entry should now be gone since
	// the refcount is 0.
	dedup.evict(now.Add(2 * time.Duration(ttlSeconds) * time.Second))

	// SeenCronRun must return false: the entry was evicted.
	if dedup.SeenCronRun("coder", "owner/repo", 0, now.Add(2*time.Duration(ttlSeconds)*time.Second)) {
		t.Error("expected SeenCronRun=false after finalize+evict: entry should be gone")
	}
}

// TestLongRunningWebhookRunBlocksCronPastTTL is a regression test for the
// case where a webhook or agents.run execution outlasts its dedup_window_seconds
// TTL. Without the webhookRefCounts guard in TryClaimForCron, the cron path
// would see an expired expiresAt, consider the slot free, and proceed concurrently
// with the still-in-flight webhook run, breaking the fleet-wide dedup contract.
func TestLongRunningWebhookRunBlocksCronPastTTL(t *testing.T) {
	t.Parallel()

	// Short TTL so we can simulate expiry without real sleeps.
	const ttlSeconds = 1
	dedup := NewDispatchDedupStore(ttlSeconds)

	start := time.Now()

	// Webhook run claims the slot and immediately marks itself in-flight, as the
	// fanOut path does after a successful TryClaimForDispatch.
	if !dedup.TryClaimForDispatch("coder", "owner/repo", 42, start) {
		t.Fatal("TryClaimForDispatch: expected claim to succeed on fresh store")
	}
	dedup.CommitClaim("coder", "owner/repo", 42)
	dedup.MarkWebhookRunInFlight("coder", "owner/repo", 42)

	// Advance past the TTL and run the sweeper. The entry must survive because
	// webhookRefCounts["coder\x00owner/repo\x0042"] is still 1.
	pastTTL := start.Add(2 * time.Duration(ttlSeconds) * time.Second)
	dedup.evict(pastTTL)

	// A cron tick arriving after the TTL window must still be suppressed because
	// the webhook run is still in flight (refcount > 0). Before the fix,
	// TryClaimForCron would return true here, racing the in-flight run.
	if dedup.TryClaimForCron("coder", "owner/repo", 42, pastTTL) {
		t.Error("TryClaimForCron must return false: webhook run still in flight past TTL")
	}

	// Once the run completes and the entry is evicted, the cron tick must proceed.
	dedup.FinalizeWebhookRun("coder", "owner/repo", 42)
	dedup.evict(pastTTL) // refcount is now 0; expiresAt already elapsed → evicted.

	if !dedup.TryClaimForCron("coder", "owner/repo", 42, pastTTL) {
		t.Error("TryClaimForCron must return true: webhook run completed and entry evicted")
	}
}

// TestDispatcherReflectsLiveAllowlistChanges verifies that the dispatcher
// reads the agent's allow_dispatch flag from SQLite on every dispatch
// (replacing the pre-cutover hot-reload UpdateAgents call): updating the
// flag in the database between two dispatch calls is observed by the
// second one without a restart.
func TestDispatcherReflectsLiveAllowlistChanges(t *testing.T) {
	t.Parallel()
	q := &fakeQueue{}
	st := dispatchTestStore(t)
	d := NewDispatcher(testDispatchCfg(), st, NewDispatchDedupStore(300), q, zerolog.Nop())

	// Initially "sec-reviewer" has AllowDispatch: false — dispatch is dropped.
	originator := originatorAgent("coder")
	originator.CanDispatch = append(originator.CanDispatch, "sec-reviewer")
	ev := testEvent("owner/repo", 1)

	d.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
		{Agent: "sec-reviewer", Reason: "initial — should be dropped"},
	})
	if len(q.popped()) != 0 {
		t.Error("expected dispatch dropped: sec-reviewer has allow_dispatch: false")
	}

	// Live update: flip sec-reviewer to allow dispatch by writing to SQLite.
	if err := st.UpsertAgent(fleet.Agent{
		Name: "sec-reviewer", Backend: "claude", Prompt: "review",
		Description: "test", AllowDispatch: true,
	}); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	d.ProcessDispatches(context.Background(), originator, ev, "root-2", 0, "", []ai.DispatchRequest{
		{Agent: "sec-reviewer", Reason: "after update — should be enqueued"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued: sec-reviewer now has allow_dispatch: true")
	}
}
