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

func TestTriggerAgentRejectsUnboundAgent(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) {
		c.Agents = append(c.Agents, config.AgentDef{Name: "orphan", Backend: "claude", Prompt: "x"})
	})
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "orphan", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("expected not-bound error, got %v", err)
	}
}

func TestTriggerAgentRejectsUnknownAgent(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "ghost", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestTriggerAgentRejectsDisabledRepo(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(func(c *config.Config) { c.Repos[0].Enabled = false })
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": &stubRunner{}}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	err = s.TriggerAgent(context.Background(), "reviewer", "owner/repo")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected disabled error, got %v", err)
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

// promptCapturingRunner records the prompt from each Run call for inspection.
type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptCapturingRunner) Run(_ context.Context, req ai.Request) (ai.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, req.Prompt)
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
		_ = dispatcher.ProcessDispatches(context.Background(), cfg.Agents[0], ev, "root-id", 0,
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

// TestSchedulerCronMarkNotWrittenOnBackendResolutionFailure verifies that if
// executeAgentRun fails early (bad backend / missing runner) the cron-namespace
// mark is never written. Without this guarantee a transient config error would
// leave a stale MarkAutonomousRun entry that suppresses all autonomous-context
// dispatches for the full dedup_window_seconds even though the agent never ran.
func TestSchedulerCronMarkNotWrittenOnBackendResolutionFailure(t *testing.T) {
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

	// Register no runners — runner-lookup will fail for every backend.
	s, err := NewScheduler(cfg, map[string]ai.Runner{}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// TriggerAgent must fail (no runner for "claude").
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err == nil {
		t.Fatal("TriggerAgent: expected error (no runner registered), got nil")
	}

	// Despite the failure, a subsequent autonomous-context dispatch targeting
	// reviewer (number=0) must NOT be suppressed — no cron mark should have
	// been written because the run never proceeded past runner-resolution.
	originator := agentMap["notifier"]
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
	dispatcher.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "retry after config fix"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued: no cron mark should have been written on runner-resolution failure")
	}
}

// TestSchedulerCronMarkNotWrittenOnRunnerFailure verifies that if runner.Run
// returns an error, the cron-namespace mark is never written. Without this
// guarantee a transient runner error would leave a stale MarkAutonomousRun
// entry that suppresses autonomous-context dispatches for the full
// dedup_window_seconds even though no run completed.
func TestSchedulerCronMarkNotWrittenOnRunnerFailure(t *testing.T) {
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

	runErr := errors.New("backend unavailable")
	failRunner := &errorRunner{err: runErr}
	q := &fakeQueue{}
	agentMap := map[string]config.AgentDef{
		"reviewer": cfg.Agents[0],
		"notifier": cfg.Agents[1],
	}
	dedup := workflow.NewDispatchDedupStore(300)
	dispatchCfg := config.DispatchConfig{MaxDepth: 3, MaxFanout: 4, DedupWindowSeconds: 300}
	dispatcher := workflow.NewDispatcher(dispatchCfg, agentMap, dedup, q, zerolog.Nop())

	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": failRunner}, NewMemoryStore(t.TempDir()), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithDispatcher(dispatcher)

	// TriggerAgent must fail because the runner returns an error.
	if err := s.TriggerAgent(context.Background(), "reviewer", "owner/repo"); err == nil {
		t.Fatal("TriggerAgent: expected error from runner, got nil")
	}

	// Despite the failure, a subsequent autonomous-context dispatch targeting
	// reviewer (number=0) must NOT be suppressed — no cron mark should have
	// been written because runner.Run never succeeded.
	originator := agentMap["notifier"]
	ev := workflow.Event{Repo: workflow.RepoRef{FullName: "owner/repo", Enabled: true}, Kind: "autonomous", Number: 0}
	dispatcher.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "retry after transient failure"},
	})
	if len(q.popped()) != 1 {
		t.Error("expected dispatch enqueued: no cron mark should have been written on runner failure")
	}
}

// errorRunner satisfies ai.Runner and always returns the configured error.
type errorRunner struct {
	err error
}

func (r *errorRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{}, r.err
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
	dispatcher2.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, []ai.DispatchRequest{
		{Agent: "reviewer", Reason: "follow-up dispatch"},
	})
	if len(q2.popped()) != 0 {
		t.Error("dispatch should be suppressed: cron mark must survive a post-run enqueue failure")
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
	_ = dispatcher.ProcessDispatches(context.Background(), originator, ev, "root-1", 0, []ai.DispatchRequest{
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
