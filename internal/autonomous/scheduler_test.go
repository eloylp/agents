package autonomous

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
	"github.com/eloylp/agents/internal/workflow"
)

type stubRunner struct {
	mu        sync.Mutex
	calls     int
	workflows []string
}

func (s *stubRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.workflows = append(s.workflows, req.Workflow)
	return ai.Response{}, nil
}

// blockingRunner signals on ready when a run starts, then blocks until block is closed.
type blockingRunner struct {
	mu    sync.Mutex
	calls int
	ready chan struct{}
	block chan struct{}
}

func (b *blockingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.ready <- struct{}{}
	<-b.block
	return ai.Response{}, nil
}

// errorRunner satisfies ai.Runner and always returns the configured error.
type errorRunner struct {
	err error
}

func (r *errorRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{}, r.err
}

// baseCfg returns a minimal valid Config suitable for scheduler tests. Use
// `modify` to tailor the repo bindings.
func baseCfg(modify func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude"},
			},
			MemoryDir: "/tmp/agent-memory",
		},
		Skills: map[string]config.SkillDef{
			"architect": {Prompt: "Focus on architecture."},
		},
		Agents: []config.AgentDef{
			{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review PRs."},
		},
		Repos: []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "reviewer", Cron: "* * * * *"},
				},
			},
		},
	}
	if modify != nil {
		modify(cfg)
	}
	return cfg
}

func TestNewSchedulerEntryRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantCount int
	}{
		{
			name:      "cron binding registered",
			wantCount: 1,
		},
		{
			name:      "skips disabled repo",
			mutate:    func(c *config.Config) { c.Repos[0].Enabled = false },
			wantCount: 0,
		},
		{
			name: "skips disabled binding",
			mutate: func(c *config.Config) {
				f := false
				c.Repos[0].Use[0].Enabled = &f
			},
			wantCount: 0,
		},
		{
			name: "skips label-only binding",
			mutate: func(c *config.Config) {
				c.Repos[0].Use[0] = config.Binding{Agent: "reviewer", Labels: []string{"ai:review"}}
			},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := NewScheduler(baseCfg(tc.mutate), map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if len(s.agentEntries) != tc.wantCount {
				t.Errorf("agentEntries = %d, want %d", len(s.agentEntries), tc.wantCount)
			}
		})
	}
}

func TestTriggerAgentRunsSynchronously(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	runner := &stubRunner{}
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Errorf("expected 1 runner call, got %d", runner.calls)
	}
	if len(runner.workflows) == 0 || !strings.HasPrefix(runner.workflows[0], "autonomous:claude:reviewer") {
		t.Errorf("unexpected workflow tag: %v", runner.workflows)
	}
}

func TestTriggerAgentRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		mutateCfg func(*config.Config)
		agent     string
		wantErr   string
	}{
		{
			name: "unbound agent",
			mutateCfg: func(c *config.Config) {
				c.Agents = append(c.Agents, config.AgentDef{Name: "orphan", Backend: "claude", Prompt: "x"})
			},
			agent:   "orphan",
			wantErr: "not bound",
		},
		{
			name:    "unknown agent",
			agent:   "ghost",
			wantErr: "not found",
		},
		{
			name:      "disabled repo",
			mutateCfg: func(c *config.Config) { c.Repos[0].Enabled = false },
			agent:     "reviewer",
			wantErr:   "disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseCfg(tc.mutateCfg)
			s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			err = s.TriggerAgent(context.Background(), tc.agent, "owner/repo")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResolveBackendAutoFallsBackToDefault(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) { c.Agents[0].Backend = "auto" })
	runner := &stubRunner{}
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Errorf("expected auto to resolve to claude and run once, got %d calls", runner.calls)
	}
}

func TestSchedulerSkipsJobWhenPreviousRunStillRunning(t *testing.T) {
	t.Parallel()
	ready := make(chan struct{}, 1)
	block := make(chan struct{})
	runner := &blockingRunner{ready: ready, block: block}
	cfg := baseCfg(nil)

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	wrappedJob := s.cron.Entry(s.agentEntries[0].cronID).WrappedJob

	done := make(chan struct{})
	go func() {
		defer close(done)
		wrappedJob.Run()
	}()
	<-ready
	// Second invocation must be skipped while the first is still running.
	wrappedJob.Run()
	close(block)
	<-done

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 invocation (second skipped), got %d", calls)
	}
}

// promptCapturingRunner records the System part from each Run call for
// inspection. Since skills, agent prompt, and the AllowPRs restriction all
// live in the stable System part, tests that check for those tokens inspect
// req.System.
type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptCapturingRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, req.System)
	return ai.Response{}, nil
}

// dispatchingRunner returns a fixed dispatch request on every Run call.
type dispatchingRunner struct {
	dispatches []ai.DispatchRequest
}

func (r *dispatchingRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{Summary: "done", Dispatch: r.dispatches}, nil
}

// fakeQueue records events pushed to it, satisfying workflow.EventEnqueuer.
type fakeQueue struct {
	mu     sync.Mutex
	events []workflow.Event
	err    error
}

func (q *fakeQueue) PushEvent(_ context.Context, ev workflow.Event) error {
	if q.err != nil {
		return q.err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = append(q.events, ev)
	return nil
}

func (q *fakeQueue) popped() []workflow.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]workflow.Event, len(q.events))
	copy(out, q.events)
	return out
}

// dispatchCfgForTest builds a minimal config where "reviewer" can dispatch
// "notifier" and "notifier" has allow_dispatch: true.
func dispatchCfgForTest() *config.Config {
	return baseCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{
				Name:        "reviewer",
				Backend:     "claude",
				Prompt:      "Review PRs.",
				CanDispatch: []string{"notifier"},
			},
			{
				Name:          "notifier",
				Backend:       "claude",
				Prompt:        "Notify team.",
				AllowDispatch: true,
			},
		}
		c.Repos[0].Use = []config.Binding{
			{Agent: "reviewer", Cron: "* * * * *"},
			{Agent: "notifier", Cron: "0 0 * * *"},
		}
	})
}

func TestSchedulerDispatchesEnqueuedWhenDispatcherAttached(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "review done", Number: 42},
		},
	}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatched event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != "agent.dispatch" {
		t.Errorf("event kind: got %q, want %q", ev.Kind, "agent.dispatch")
	}
	if got, ok := ev.Payload["target_agent"].(string); !ok || got != "notifier" {
		t.Errorf("target_agent: got %v, want %q", ev.Payload["target_agent"], "notifier")
	}
	if ev.Number != 42 {
		t.Errorf("event number: got %d, want 42", ev.Number)
	}
	if ev.Repo.FullName != "owner/repo" {
		t.Errorf("event repo: got %q, want %q", ev.Repo.FullName, "owner/repo")
	}
}

func TestSchedulerAutonomousDispatchCarriesNonEmptyRootEventID(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "cron done"},
		},
	}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	events := q.popped()
	if len(events) != 1 {
		t.Fatalf("expected 1 dispatched event, got %d", len(events))
	}
	ev := events[0]

	// The dispatched event must carry a non-empty root_event_id so that
	// autonomous dispatch chains preserve the correlation ID throughout.
	rootEventID, ok := ev.Payload["root_event_id"].(string)
	if !ok || rootEventID == "" {
		t.Errorf("root_event_id: got %v, want non-empty string", ev.Payload["root_event_id"])
	}
	// ev.ID is the synthetic event's own identity and must differ from root_event_id.
	if ev.ID == "" {
		t.Error("dispatch event ID must not be empty")
	}
	if ev.ID == rootEventID {
		t.Errorf("ev.ID must differ from root_event_id %q", rootEventID)
	}
}

func TestSchedulerDispatchesIgnoredWhenNoDispatcherAttached(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "review done"},
		},
	}

	// No dispatcher attached — TriggerAgent should still succeed and just log.
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}
	// No panic or error: success.
}

func TestSchedulerCronRunSkippedWhenAlreadySeenInDedup(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &stubRunner{}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// Simulate a dispatch having arrived first for (reviewer, owner/repo, 0):
	// write the dispatch-namespace dedup key directly, as ProcessDispatches
	// would. CheckAndMarkAutonomousRun checks this dispatch namespace and must
	// detect the prior dispatch and skip the cron run.
	_ = dedup.SeenOrAdd("reviewer", "owner/repo", 0, time.Now())

	// TriggerAgent must skip the run and return ErrDispatchSkipped.
	err = s.TriggerAgent(context.Background(), "reviewer", "owner/repo")
	if !errors.Is(err, ErrDispatchSkipped) {
		t.Fatalf("TriggerAgent: got %v, want ErrDispatchSkipped", err)
	}

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 0 {
		t.Errorf("expected runner not to be called (run skipped by dedup), got %d call(s)", calls)
	}
}

// blockingQueue is a workflow.EventEnqueuer whose PushEvent signals readyCh
// then blocks until releaseCh is closed. It is used to freeze the dispatch
// pipeline between TryClaim (before PushEvent) and CommitClaim (after PushEvent)
// so that concurrent scheduler checks can be tested in that exact window.
type blockingQueue struct {
	readyCh   chan struct{} // closed by PushEvent to signal "I am blocking"
	releaseCh chan struct{} // close to let PushEvent return
}

func (q *blockingQueue) PushEvent(_ context.Context, _ workflow.Event) error {
	close(q.readyCh)   // signal that TryClaim has run and we're now blocking
	<-q.releaseCh      // wait for the test to release us
	return nil
}

// TestCronRunBlockedByPendingDispatchClaim is a regression test for the race
// between a dispatch's TryClaim→CommitClaim window and a concurrent cron/manual
// run. Before the fix, DispatchAlreadyClaimed only saw committed claims; a
// cron run that checked during the pending window would see false, proceed, and
// run concurrently with the in-flight dispatch. After the fix,
// SeesPendingOrCommitted makes DispatchAlreadyClaimed return true for any
// pending or committed claim, so exactly one path wins.
func TestCronRunBlockedByPendingDispatchClaim(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	notifierRunner := &stubRunner{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}

	bq := &blockingQueue{
		readyCh:   make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, bq, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": notifierRunner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// Start a dispatch to "notifier" in a goroutine. ProcessDispatches will
	// TryClaim the (notifier, owner/repo, 0) slot, then block inside PushEvent
	// (before CommitClaim). This is the race window being tested.
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}}
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		_ = dispatcher.ProcessDispatches(context.Background(), cfg.Agents[0], ev, "root-id", 0, "",
			[]ai.DispatchRequest{{Agent: "notifier", Reason: "test"}})
	}()

	// Wait until PushEvent is blocked — at this point TryClaim has run and the
	// slot is pending (not yet committed). A cron check in this window should
	// now detect the pending claim and skip.
	<-bq.readyCh

	// TriggerAgent for "notifier" must return ErrDispatchSkipped, not proceed
	// to run the agent concurrently with the in-flight dispatch.
	triggerErr := s.TriggerAgent(context.Background(), "notifier", "owner/repo")
	if !errors.Is(triggerErr, ErrDispatchSkipped) {
		t.Errorf("TriggerAgent: got %v, want ErrDispatchSkipped", triggerErr)
	}

	// Unblock the dispatch goroutine and wait for it to finish.
	close(bq.releaseCh)
	<-dispatchDone

	notifierRunner.mu.Lock()
	calls := notifierRunner.calls
	notifierRunner.mu.Unlock()
	if calls != 0 {
		t.Errorf("expected notifier runner not to be called (blocked by pending claim), got %d call(s)", calls)
	}
}

// TestSchedulerCronRunNotSuppressedByPriorCronRun verifies that two consecutive
// cron runs for the same agent both execute. MarkAutonomousRun writes a
// cron-namespace mark (not a dispatch-namespace entry), and cron runs only check
// the dispatch namespace via DispatchAlreadyClaimed, so the cron mark never
// suppresses subsequent cron runs.
func TestSchedulerCronRunNotSuppressedByPriorCronRun(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &stubRunner{}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// First cron/manual run.
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent (1st): %v", err)
	}
	// Second cron/manual run within the same dedup window must also execute.
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent (2nd): %v", err)
	}

	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 2 {
		t.Errorf("expected runner to be called twice (both runs should execute), got %d call(s)", calls)
	}
}

// TestSchedulerCronMarkNotWrittenOnRunFailure verifies that when TriggerAgent
// fails — whether because no runner is registered for the backend, or because
// the runner itself returns an error — no cron-namespace mark is written.
// Without this guarantee a failed run would leave a stale MarkAutonomousRun
// entry that suppresses autonomous-context dispatches for the full
// dedup_window_seconds even though the agent never ran.
func TestSchedulerCronMarkNotWrittenOnRunFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		runners map[string]ai.Runner
	}{
		{
			name:    "no runner registered for backend",
			runners: map[string]ai.Runner{},
		},
		{
			name:    "runner returns error",
			runners: map[string]ai.Runner{"claude": &errorRunner{err: errors.New("backend unavailable")}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// "notifier" can dispatch to "reviewer"; "reviewer" has allow_dispatch so
			// whitelist and opt-in checks pass and we can isolate the cron-mark behavior.
			cfg := baseCfg(func(c *config.Config) {
				c.Agents = []config.AgentDef{
					{
						Name:          "reviewer",
						Backend:       "claude",
						Prompt:        "review",
						AllowDispatch: true,
					},
					{
						Name:        "notifier",
						Backend:     "claude",
						Prompt:      "notify",
						CanDispatch: []string{"reviewer"},
					},
				}
				c.Repos[0].Use = []config.Binding{
					{Agent: "reviewer", Cron: "* * * * *"},
					{Agent: "notifier", Cron: "0 0 * * *"},
				}
			})

			q := &fakeQueue{}
			agentMap := map[string]config.AgentDef{
				"reviewer": cfg.Agents[0],
				"notifier": cfg.Agents[1],
			}
			dedup := workflow.NewDispatchDedupStore(300)
			dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
			dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

			s, err := NewScheduler(cfg, tc.runners, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			s.WithDispatcher(dispatcher)

			if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err == nil {
				t.Fatal("TriggerAgent: expected error, got nil")
			}

			// Despite the failure, a subsequent autonomous-context dispatch targeting
			// reviewer (number=0) must NOT be suppressed — no cron mark should have
			// been written because the run never completed.
			originator := agentMap["notifier"]
			ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
			dispatcher.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
				{Agent: "reviewer", Reason: "retry after failure"},
			})
			if len(q.popped()) != 1 {
				t.Error("expected dispatch enqueued: no cron mark should have been written on run failure")
			}
		})
	}
}

// TestSchedulerDispatchEnqueueFailurePropagates verifies that when the event
// queue rejects an enqueue during ProcessDispatches, the error bubbles up
// through executeAgentRun and out of TriggerAgent instead of being silently
// swallowed.
func TestSchedulerDispatchEnqueueFailurePropagates(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "review done", Number: 42},
		},
	}
	queueErr := errors.New("queue full")
	q := &fakeQueue{err: queueErr}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	err = s.TriggerAgent(context.Background(), "reviewer", "owner/repo")
	if err == nil {
		t.Fatal("expected error when dispatch enqueue fails, got nil")
	}
	if !strings.Contains(err.Error(), "dispatch") {
		t.Errorf("expected 'dispatch' in error, got: %v", err)
	}
}

// TestSchedulerCronMarkKeptAfterSuccessfulRunWithDispatchEnqueueFailure verifies
// that when runner.Run succeeds but the post-run dispatch enqueue fails, the
// cron-namespace mark is NOT rolled back. The autonomous pass already committed,
// so the dedup window must stay in force to prevent a duplicate run.
func TestSchedulerCronMarkKeptAfterSuccessfulRunWithDispatchEnqueueFailure(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "review done", Number: 42},
		},
	}
	queueErr := errors.New("queue full")
	q := &fakeQueue{err: queueErr}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// TriggerAgent should return an error (dispatch enqueue failed).
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err == nil {
		t.Fatal("expected error from dispatch enqueue failure, got nil")
	}

	// runner.Run succeeded, so the cron mark must still be in place. A
	// subsequent autonomous-context dispatch targeting the same (agent, repo, 0)
	// must be suppressed — if the mark were rolled back, it would slip through.
	// Use a fresh queue (no error) and a new dispatcher sharing the same dedup
	// store so enqueue would succeed if the mark were absent.
	q2 := &fakeQueue{}
	dispatcher2 := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q2, zerolog.Nop())
	originator := agentMap["notifier"]
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
	dispatcher2.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "follow-up dispatch"},
	})
	if len(q2.popped()) != 0 {
		t.Error("dispatch should be suppressed: cron mark must survive a post-run enqueue failure")
	}
}

// TestSchedulerCronRefcountDecrementedOnPostRunEnqueueFailure verifies that
// when runner.Run succeeds but the subsequent dispatch enqueue fails,
// FinalizeAutonomousRun is still called so the cron refcount drops to zero.
// Without this fix, evict() refuses to remove the cron entry (refcount > 0)
// and TryClaimForDispatch permanently blocks autonomous-context dispatches for
// (agent, repo, 0) until process restart.
func TestSchedulerCronRefcountDecrementedOnPostRunEnqueueFailure(t *testing.T) {
	t.Parallel()
	cfg := dispatchCfgForTest()

	runner := &dispatchingRunner{
		dispatches: []ai.DispatchRequest{
			{Agent: "notifier", Reason: "review done", Number: 42},
		},
	}
	queueErr := errors.New("queue full")
	q := &fakeQueue{err: queueErr}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	// Use a 1-second TTL so we can simulate expiry with a past timestamp.
	dedup := workflow.NewDispatchDedupStore(1)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 1}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// First run — runner.Run succeeds, dispatch enqueue fails.
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err == nil {
		t.Fatal("expected error from dispatch enqueue failure, got nil")
	}

	// After the run, the cron TTL entry should have been finalized (refcount=0)
	// so that the dedup store can evict the entry once its TTL elapses. Verify
	// this indirectly: use TryClaimForDispatch with a timestamp 2 seconds past
	// the start (well past the 1-second TTL). With the bug (refcount stuck at 1),
	// TryClaimForDispatch returns false even past the TTL. With the fix (refcount=0),
	// the TTL check and the refcount check both pass and the dispatch is allowed.
	// After the run, FinalizeAutonomousRun must have been called (refcount=0).
	// Verify indirectly: once the 1-second TTL elapses, TryClaimForDispatch
	// should allow an autonomous-context dispatch to (notifier, owner/repo, 0).
	// Without the fix (refcount stuck at 1), TryClaimForDispatch returns false
	// even past the TTL because it checks cronRefCounts > 0 as a secondary guard.
	//
	// Note: "reviewer" can dispatch "notifier" (CanDispatch) and "notifier" has
	// AllowDispatch: true — so we use reviewer as originator dispatching notifier,
	// mirroring the original run that wrote the cron mark for "notifier".
	// We need to trigger "notifier" (not "reviewer") to see the dedup effect
	// for the cron mark written for notifier's slot.
	//
	// Actually, the first run was for "reviewer" (TriggerAgent("reviewer", ...)),
	// so the cron mark is at ("reviewer", "owner/repo", 0). To test that this mark
	// no longer blocks dispatches to "reviewer", we need reviewer to have
	// AllowDispatch: true. Add a local config that grants this.
	agentMapWithAllowDispatch := map[string]config.AgentDef{
		"reviewer": {
			Name:          "reviewer",
			Backend:       "claude",
			Prompt:        "Review PRs.",
			AllowDispatch: true,
		},
		"notifier": {
			Name:        "notifier",
			Backend:     "claude",
			Prompt:      "Notify team.",
			CanDispatch: []string{"reviewer"},
		},
	}
	q2 := &fakeQueue{}
	dispatcher2 := workflow.NewDispatcher(dispatchCfg, agentMapWithAllowDispatch, dedup, q2, zerolog.Nop())
	originator := agentMapWithAllowDispatch["notifier"]
	ev := workflow.Event{
		Repo:  workflow.RepoRef{FullName: "owner/repo", Enabled: true},
		Kind:  "autonomous",
		Actor: "notifier",
	}
	time.Sleep(2 * time.Second) // wait for the 1-second TTL to expire
	dispatcher2.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "follow-up dispatch"},
	})
	if len(q2.popped()) == 0 {
		t.Error("dispatch should succeed after TTL elapsed and refcount finalized: permanent suppression bug detected")
	}
}

// TestSchedulerPostRunDispatchSuppressedWithinDedupWindow is an integration-level
// regression test for the cron-first dedup ordering: after a cron/manual run
// completes successfully, dispatches targeting the same (agent, repo, 0) context
// must still be suppressed for the full dedup_window_seconds. FinalizeAutonomousRun
// decrements the in-flight refcount but preserves the cron-namespace TTL entry so
// TryClaimForDispatch continues to reject dispatches until the window expires.
func TestSchedulerPostRunDispatchSuppressedWithinDedupWindow(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{
				Name:          "notifier",
				Backend:       "claude",
				Prompt:        "Notify.",
				AllowDispatch: true,
			},
			{
				Name:        "reviewer",
				Backend:     "claude",
				Prompt:      "Review.",
				CanDispatch: []string{"notifier"},
			},
		}
		c.Repos[0].Use = []config.Binding{
			{Agent: "notifier", Cron: "* * * * *"},
			{Agent: "reviewer", Cron: "0 0 * * *"},
		}
	})

	runner := &stubRunner{}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"notifier": cfg.Agents[0],
		"reviewer": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300) // 5-minute window
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// Cron/manual run completes successfully.
	if err := s.TriggerAgent(context.Background(), "notifier", "owner/repo"); err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	// Within the dedup window, a dispatch to the same (notifier, owner/repo, 0)
	// autonomous context must be suppressed. The cron-namespace TTL entry is
	// preserved by FinalizeAutonomousRun, not cleared, so TryClaimForDispatch
	// returns false and the dispatch is dropped.
	q2 := &fakeQueue{}
	dispatcher2 := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q2, zerolog.Nop())
	originator := agentMap["reviewer"]
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
	dispatcher2.ProcessDispatches(context.Background(), originator, ev, "root-post-run", 0, "", []ai.DispatchRequest{
		{Agent: "notifier", Reason: "follow-up dispatch within dedup window"},
	})
	if len(q2.popped()) != 0 {
		t.Error("expected dispatch suppressed: cron-namespace TTL entry must persist after successful run")
	}
}

func TestSchedulerAllowPRsPromptPrefixing(t *testing.T) {
	t.Parallel()
	const noPRPrefix = "Do not open or create pull requests under any circumstances."
	tests := []struct {
		name     string
		allowPRs bool
		prompt   string
		wantNoPR bool
	}{
		{
			name:     "no-PR instruction prepended when allow_prs=false",
			allowPRs: false,
			prompt:   "Review PRs.",
			wantNoPR: true,
		},
		{
			name:     "no-PR instruction absent when allow_prs=true",
			allowPRs: true,
			prompt:   "Open a PR with the fix.",
			wantNoPR: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &promptCapturingRunner{}
			cfg := baseCfg(func(c *config.Config) {
				c.Agents = []config.AgentDef{
					{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: tc.prompt, AllowPRs: tc.allowPRs},
				}
			})
			s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err != nil {
				t.Fatalf("TriggerAgent: %v", err)
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if len(runner.prompts) != 1 {
				t.Fatalf("expected 1 prompt, got %d", len(runner.prompts))
			}
			hasNoPR := strings.Contains(runner.prompts[0], noPRPrefix)
			if hasNoPR != tc.wantNoPR {
				t.Errorf("no-PR prefix present=%v, want %v; prompt: %q", hasNoPR, tc.wantNoPR, runner.prompts[0])
			}
			if !strings.Contains(runner.prompts[0], tc.prompt) {
				t.Errorf("expected original prompt text %q to be present, got: %q", tc.prompt, runner.prompts[0])
			}
		})
	}
}

// TestSchedulerCronMarkBlocksDispatchDuringInFlightRun verifies that when an
// autonomous run is in progress, a dispatch arriving for the same (agent, repo,
// 0) context is suppressed by the cron-namespace mark that is written before
// runner.Run is called. Without a pre-run mark, the dispatch would slip through
// before the mark is written (after runner.Run) and enqueue a concurrent run.
func TestSchedulerCronMarkBlocksDispatchDuringInFlightRun(t *testing.T) {
	t.Parallel()

	cfg := baseCfg(func(c *config.Config) {
		c.Agents = []config.AgentDef{
			{
				Name:          "reviewer",
				Backend:       "claude",
				Prompt:        "review",
				AllowDispatch: true,
			},
			{
				Name:        "notifier",
				Backend:     "claude",
				Prompt:      "notify",
				CanDispatch: []string{"reviewer"},
			},
		}
		c.Repos[0].Use = []config.Binding{
			{Agent: "reviewer", Cron: "* * * * *"},
			{Agent: "notifier", Cron: "0 0 * * *"},
		}
	})

	ready := make(chan struct{}, 1)
	block := make(chan struct{})
	runner := &blockingRunner{ready: ready, block: block}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// Start the autonomous run in the background; wait until the runner is
	// actually inside Run (i.e., the cron mark should already be written).
	done := make(chan error, 1)
	go func() {
		done <- s.TriggerAgent(context.Background(), "reviewer", "owner/repo")
	}()
	<-ready // blockingRunner signals here, so the cron mark is in place

	// Simulate notifier dispatching to reviewer while the run is in progress.
	originator := agentMap["notifier"]
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
	_ = dispatcher.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, "", []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "arrived during in-flight run"},
	})

	// Unblock the runner and wait for TriggerAgent to return.
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("TriggerAgent: %v", err)
	}

	// The dispatch must have been suppressed (deduped), so no event was enqueued.
	if len(q.popped()) != 0 {
		t.Errorf("expected dispatch suppressed by in-flight cron mark, but %d event(s) were enqueued", len(q.popped()))
	}
}

// TestCronDispatchCrossNamespaceRaceOnlyOneWins is a regression test for the
// TOCTOU race identified after the initial dispatch-dedup implementation. The
// old code used two separate operations:
//
//	cron path:     DispatchAlreadyClaimed (read) → ... → MarkAutonomousRun (write)
//	dispatch path: SeenCronRun (read)            → ... → TryClaim (write)
//
// If the reads on both sides completed before either write, both paths observed
// "no opposing claim" and proceeded concurrently for the same (agent, repo, 0)
// slot. The fix replaces each pair with a single mutex-held TryClaimForCron /
// TryClaimForDispatch call that atomically checks the cross-namespace and writes
// the reservation, so only one path can ever win per (agent, repo, 0) slot.
//
// This test races TriggerAgent (cron path) against ProcessDispatches (dispatch
// path) for the same agent/repo over many iterations and asserts that the two
// paths never both execute in the same round.
func TestCronDispatchCrossNamespaceRaceOnlyOneWins(t *testing.T) {
	t.Parallel()
	const iterations = 500
	ctx := context.Background()

	cfg := dispatchCfgForTest()
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}

	for i := range iterations {
		// Fresh dedup store, queue, runner, and scheduler for each iteration so
		// each round starts from an empty state.
		dedup := workflow.NewDispatchDedupStore(300)
		dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
		q := &fakeQueue{}
		dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

		runner := &stubRunner{}
		s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": runner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
		if err != nil {
			t.Fatalf("iteration %d: NewScheduler: %v", i, err)
		}
		s.WithDispatcher(dispatcher)

		// Race: cron run (TriggerAgent for notifier) vs dispatch to notifier.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.TriggerAgent(ctx, "notifier", "owner/repo")
		}()
		go func() {
			defer wg.Done()
			ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}}
			_ = dispatcher.ProcessDispatches(ctx, cfg.Agents[0], ev, "root", 0, "",
				[]ai.DispatchRequest{{Agent: "notifier", Reason: "concurrent dispatch"}})
		}()
		wg.Wait()

		runner.mu.Lock()
		calls := runner.calls
		runner.mu.Unlock()
		enqueued := len(q.popped())

		// Both executing concurrently is the race: runner called AND dispatch
		// enqueued for the same (notifier, owner/repo, 0) slot in the same round.
		if calls >= 1 && enqueued >= 1 {
			t.Fatalf("iteration %d: TOCTOU race — cron ran (%d call(s)) AND dispatch enqueued (%d event(s)) concurrently for the same slot",
				i, calls, enqueued)
		}
	}
}

// TestSchedulerReload verifies that Reload replaces cron registrations with
// those from the updated config slices, and that AgentStatuses reflects the
// new registrations.
func TestSchedulerReload(t *testing.T) {
	t.Parallel()

	// Start with a config that has one cron binding.
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	statuses := s.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("before reload: got %d statuses, want 1", len(statuses))
	}

	// Reload with a completely different agent and repo.
	newAgents := []config.AgentDef{
		{Name: "scanner", Backend: "claude", Skills: []string{}, Prompt: "scan"},
	}
	newRepos := []config.RepoDef{
		{
			Name:    "owner/other",
			Enabled: true,
			Use:     []config.Binding{{Agent: "scanner", Cron: "* * * * *"}},
		},
	}
	if err := s.Reload(newRepos, newAgents, cfg.Skills, map[string]config.AIBackendConfig{"claude": {Command: "claude"}}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	statuses = s.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("after reload: got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Name != "scanner" {
		t.Errorf("after reload: agent name %q, want %q", statuses[0].Name, "scanner")
	}
	if statuses[0].Repo != "owner/other" {
		t.Errorf("after reload: repo %q, want %q", statuses[0].Repo, "owner/other")
	}
}

// TestSchedulerReloadUpdatesSkillsAndBackends verifies that Reload replaces
// cfg.Skills and cfg.Daemon.AIBackends so that future agent runs pick up the
// new definitions without requiring a daemon restart.
func TestSchedulerReloadUpdatesSkillsAndBackends(t *testing.T) {
	t.Parallel()

	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	newSkills := map[string]config.SkillDef{
		"security": {Prompt: "Think about security."},
	}
	newBackends := map[string]config.AIBackendConfig{
		"claude": {Command: "claude", TimeoutSeconds: 120},
	}

	if err := s.Reload(cfg.Repos, cfg.Agents, newSkills, newBackends); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if len(s.cfg.Skills) != 1 || s.cfg.Skills["security"].Prompt != "Think about security." {
		t.Errorf("after reload: cfg.Skills = %v, want {security: ...}", s.cfg.Skills)
	}
	if s.cfg.Daemon.AIBackends["claude"].TimeoutSeconds != 120 {
		t.Errorf("after reload: claude backend timeout = %d, want 120",
			s.cfg.Daemon.AIBackends["claude"].TimeoutSeconds)
	}
}

// TestSchedulerReloadClearsAllBindings verifies that a Reload with no cron
// bindings removes all previously registered entries.
func TestSchedulerReloadClearsAllBindings(t *testing.T) {
	t.Parallel()

	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Reload with repos that have no cron bindings.
	if err := s.Reload([]config.RepoDef{{Name: "owner/repo", Enabled: true, Use: []config.Binding{
		{Agent: "reviewer", Labels: []string{"ai:fix"}},
	}}}, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	statuses := s.AgentStatuses()
	if len(statuses) != 0 {
		t.Errorf("after reload with no cron: got %d statuses, want 0", len(statuses))
	}
}

// TestSchedulerReloadRollsBackOnFailure verifies that a Reload that cannot
// register new cron entries (e.g. because a binding references an unknown
// agent) preserves the previous scheduler state instead of leaving it empty.
func TestSchedulerReloadRollsBackOnFailure(t *testing.T) {
	t.Parallel()

	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	// Capture the pre-reload statuses.
	before := s.AgentStatuses()
	if len(before) != 1 {
		t.Fatalf("before reload: got %d statuses, want 1", len(before))
	}

	// Attempt a reload where the binding references an agent not in the agents slice.
	badRepos := []config.RepoDef{{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []config.Binding{{Agent: "ghost", Cron: "* * * * *"}},
	}}
	badAgents := []config.AgentDef{} // "ghost" not in this list → registerJobs will fail

	if err := s.Reload(badRepos, badAgents, cfg.Skills, cfg.Daemon.AIBackends); err == nil {
		t.Fatal("Reload: expected error for unknown agent binding, got nil")
	}

	// The scheduler must still have the original entries, not be empty.
	after := s.AgentStatuses()
	if len(after) != len(before) {
		t.Errorf("after failed reload: got %d statuses, want %d (original preserved)",
			len(after), len(before))
	}
	if len(after) > 0 && after[0].Name != before[0].Name {
		t.Errorf("after failed reload: agent %q, want %q (original preserved)",
			after[0].Name, before[0].Name)
	}
	// Config must also be rolled back.
	if len(s.cfg.Agents) != len(cfg.Agents) {
		t.Errorf("after failed reload: cfg.Agents len=%d, want %d (original preserved)",
			len(s.cfg.Agents), len(cfg.Agents))
	}
}
