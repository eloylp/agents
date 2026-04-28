package autonomous

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// mapMemory is an in-memory MemoryBackend for tests that don't need a real DB.
type mapMemory struct {
	mu   sync.Mutex
	data map[string]string
}

func newMapMemory() *mapMemory { return &mapMemory{data: make(map[string]string)} }

func (m *mapMemory) ReadMemory(agent, repo string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[agent+"\x00"+repo], nil
}

func (m *mapMemory) WriteMemory(agent, repo, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[agent+"\x00"+repo] = content
	return nil
}

// stubRunner satisfies ai.Runner for the reload tests that need to construct a
// scheduler. Cron-fired runs no longer execute through this runner — they are
// pushed to the event queue and the engine handles them — so the runner here
// only exists to satisfy the runners map contract during construction.
//
// The id field is a sentinel: two empty structs may share an address under
// Go's zero-size variable rule, which would make newStubRunner() == newStubRunner()
// and break the rebuild-detected-by-pointer-identity check below.
type stubRunner struct{ id int }

func (s *stubRunner) Run(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

var stubRunnerSeq atomic.Int64

func newStubRunner() *stubRunner {
	return &stubRunner{id: int(stubRunnerSeq.Add(1))}
}

// testHotReloadSink is a minimal HotReloadSink for tests.
type testHotReloadSink struct {
	updateConfig          func(*config.Config)
	updateRunners         func(map[string]ai.Runner)
	updateConfigAndRunner func(*config.Config, map[string]ai.Runner)
}

func (s *testHotReloadSink) UpdateConfig(cfg *config.Config) {
	if s.updateConfig != nil {
		s.updateConfig(cfg)
	}
}
func (s *testHotReloadSink) UpdateRunners(r map[string]ai.Runner) {
	if s.updateRunners != nil {
		s.updateRunners(r)
	}
}
func (s *testHotReloadSink) UpdateConfigAndRunners(cfg *config.Config, r map[string]ai.Runner) {
	if s.updateConfigAndRunner != nil {
		s.updateConfigAndRunner(cfg, r)
	}
}

// drainQueue reads up to n events from a buffered DataChannels without
// blocking past the timeout. Used by cron-tick tests to assert the pushed
// event shape.
func drainQueue(t *testing.T, dc *workflow.DataChannels, n int) []workflow.Event {
	t.Helper()
	out := make([]workflow.Event, 0, n)
	deadline := time.After(100 * time.Millisecond)
	for len(out) < n {
		select {
		case ev := <-dc.EventChan():
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

// baseCfg returns a minimal valid Config suitable for scheduler tests.
func baseCfg(modify func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
		Skills: map[string]fleet.Skill{
			"architect": {Prompt: "Focus on architecture."},
		},
		Agents: []fleet.Agent{
			{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review PRs."},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
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
				c.Repos[0].Use[0] = fleet.Binding{Agent: "reviewer", Labels: []string{"ai:review"}}
			},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := NewScheduler(baseCfg(tc.mutate), map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if len(s.agentEntries) != tc.wantCount {
				t.Errorf("agentEntries = %d, want %d", len(s.agentEntries), tc.wantCount)
			}
		})
	}
}

// TestCronTickPushesEvent verifies the producer-mode contract: when a cron
// closure fires, the scheduler pushes a "cron" event with the right repo,
// agent, and target_agent payload onto the queue and returns immediately.
// Engine handling of that event is exercised in workflow/engine_test.go.
func TestCronTickPushesEvent(t *testing.T) {
	t.Parallel()
	s, err := NewScheduler(baseCfg(nil), map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	q := workflow.NewDataChannels(4)
	s.WithEventQueue(q)

	// Fire the cron closure exactly once. In production the cron library
	// invokes this on each tick.
	s.cron.Entry(s.agentEntries[0].cronID).WrappedJob.Run()

	events := drainQueue(t, q, 1)
	if len(events) != 1 {
		t.Fatalf("queue events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != "cron" {
		t.Errorf("Kind = %q, want %q", ev.Kind, "cron")
	}
	if ev.Repo.FullName != "owner/repo" || !ev.Repo.Enabled {
		t.Errorf("Repo = %+v, want owner/repo enabled", ev.Repo)
	}
	if target, _ := ev.Payload["target_agent"].(string); target != "reviewer" {
		t.Errorf("payload.target_agent = %v, want reviewer", ev.Payload["target_agent"])
	}
	if ev.Actor != "reviewer" {
		t.Errorf("Actor = %q, want reviewer", ev.Actor)
	}
}

// TestRecordLastRunUpdatesAgentStatuses verifies the LastRunRecorder hook the
// engine calls when a cron run completes. The status surfaces in /agents via
// AgentStatuses() so the schedule view stays current.
func TestRecordLastRunUpdatesAgentStatuses(t *testing.T) {
	t.Parallel()
	s, err := NewScheduler(baseCfg(nil), map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	at := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	s.RecordLastRun("reviewer", "owner/repo", at, "success")

	statuses := s.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	if statuses[0].LastStatus != "success" {
		t.Errorf("LastStatus = %q, want success", statuses[0].LastStatus)
	}
	if statuses[0].LastRun == nil || !statuses[0].LastRun.Equal(at) {
		t.Errorf("LastRun = %v, want %v", statuses[0].LastRun, at)
	}
}

func TestSchedulerReload(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	statuses := s.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("before reload: got %d statuses, want 1", len(statuses))
	}

	newAgents := []fleet.Agent{
		{Name: "scanner", Backend: "claude", Skills: []string{}, Prompt: "scan"},
	}
	newRepos := []fleet.Repo{
		{
			Name:    "owner/other",
			Enabled: true,
			Use:     []fleet.Binding{{Agent: "scanner", Cron: "* * * * *"}},
		},
	}
	if err := s.Reload(newRepos, newAgents, cfg.Skills, map[string]fleet.Backend{"claude": {Command: "claude"}}); err != nil {
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
// cfg.Skills and cfg.Daemon.AIBackends so future runs pick up the new
// definitions without a daemon restart.
func TestSchedulerReloadUpdatesSkillsAndBackends(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	newSkills := map[string]fleet.Skill{
		"security": {Prompt: "Think about security."},
	}
	newBackends := map[string]fleet.Backend{
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
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := s.Reload([]fleet.Repo{{Name: "owner/repo", Enabled: true, Use: []fleet.Binding{
		{Agent: "reviewer", Labels: []string{"ai:fix"}},
	}}}, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if statuses := s.AgentStatuses(); len(statuses) != 0 {
		t.Errorf("after reload with no cron: got %d statuses, want 0", len(statuses))
	}
}

// TestSchedulerReloadRollsBackOnFailure verifies that a Reload that cannot
// register new cron entries preserves the previous scheduler state.
func TestSchedulerReloadRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	before := s.AgentStatuses()
	if len(before) != 1 {
		t.Fatalf("before reload: got %d statuses, want 1", len(before))
	}

	badRepos := []fleet.Repo{{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "ghost", Cron: "* * * * *"}},
	}}
	badAgents := []fleet.Agent{}

	if err := s.Reload(badRepos, badAgents, cfg.Skills, cfg.Daemon.AIBackends); err == nil {
		t.Fatal("Reload: expected error for unknown agent binding, got nil")
	}

	after := s.AgentStatuses()
	if len(after) != len(before) {
		t.Errorf("after failed reload: got %d statuses, want %d (original preserved)",
			len(after), len(before))
	}
	if len(after) > 0 && after[0].Name != before[0].Name {
		t.Errorf("after failed reload: agent %q, want %q (original preserved)",
			after[0].Name, before[0].Name)
	}
	if len(s.cfg.Agents) != len(cfg.Agents) {
		t.Errorf("after failed reload: cfg.Agents len=%d, want %d (original preserved)",
			len(s.cfg.Agents), len(cfg.Agents))
	}
}

// TestSchedulerReloadRebuildsRunners verifies that when a RunnerBuilder is
// registered, Reload builds a fresh runner map to reflect new or changed
// backend definitions.
func TestSchedulerReloadRebuildsRunners(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	oldRunner := newStubRunner()
	initialRunners := map[string]ai.Runner{"claude": oldRunner}

	s, err := NewScheduler(cfg, initialRunners, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	var sinkMu sync.Mutex
	var sinkRunners map[string]ai.Runner
	sink := &testHotReloadSink{
		updateConfigAndRunner: func(_ *config.Config, r map[string]ai.Runner) {
			sinkMu.Lock()
			sinkRunners = r
			sinkMu.Unlock()
		},
	}
	s.WithHotReloadSink(sink)

	buildCalls := map[string]*stubRunner{}
	s.WithRunnerBuilder(func(name string, _ fleet.Backend) ai.Runner {
		r := newStubRunner()
		buildCalls[name] = r
		return r
	})

	newBackends := map[string]fleet.Backend{
		"claude": {Command: "claude"},
		"codex":  {Command: "codex"},
	}
	if err := s.Reload(cfg.Repos, cfg.Agents, cfg.Skills, newBackends); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := buildCalls["claude"]; !ok {
		t.Error("RunnerBuilder was not called for 'claude'")
	}
	if _, ok := buildCalls["codex"]; !ok {
		t.Error("RunnerBuilder was not called for 'codex'")
	}
	if _, ok := s.runners["claude"]; !ok {
		t.Error("scheduler runners missing 'claude' after Reload")
	}
	if _, ok := s.runners["codex"]; !ok {
		t.Error("scheduler runners missing 'codex' after Reload")
	}
	if s.runners["claude"] == oldRunner {
		t.Error("scheduler runners['claude'] was not rebuilt by RunnerBuilder")
	}
	if _, ok := initialRunners["codex"]; ok {
		t.Error("original runners map was mutated in place; expected copy-on-write")
	}
	sinkMu.Lock()
	got := sinkRunners
	sinkMu.Unlock()
	if got == nil {
		t.Fatal("HotReloadSink.UpdateConfigAndRunners was not called")
	}
	if _, ok := got["claude"]; !ok {
		t.Error("sink runners missing 'claude'")
	}
	if _, ok := got["codex"]; !ok {
		t.Error("sink runners missing 'codex'")
	}
}

// TestSchedulerReloadConfigCopyOnWrite verifies that Reload does not mutate the
// original *config.Config pointer.
func TestSchedulerReloadConfigCopyOnWrite(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	originalPtr := cfg
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	newRepos := []fleet.Repo{{Name: "owner/new-repo", Enabled: true, Use: []fleet.Binding{{Agent: "reviewer", Cron: "* * * * *"}}}}
	if err := s.Reload(newRepos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if len(originalPtr.Repos) != 1 || originalPtr.Repos[0].Name != "owner/repo" {
		t.Errorf("original config.Repos was mutated: got %v", originalPtr.Repos)
	}
	if s.cfg == cfg {
		t.Error("scheduler still holds the original config pointer; expected copy-on-write swap")
	}
	if len(s.cfg.Repos) != 1 || s.cfg.Repos[0].Name != "owner/new-repo" {
		t.Errorf("scheduler config.Repos not updated: got %v", s.cfg.Repos)
	}
}

// TestSchedulerReloadRaceWithConcurrentReads runs Reload in parallel with the
// read paths the daemon hits concurrently — AgentStatuses (called by /agents,
// /status) and the cron closures fired by the cron library. Run with -race
// to catch concurrent map/struct accesses against the cfg + runners snapshot.
func TestSchedulerReloadRaceWithConcurrentReads(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithRunnerBuilder(func(_ string, _ fleet.Backend) ai.Runner { return newStubRunner() })
	s.WithHotReloadSink(&testHotReloadSink{})
	s.WithEventQueue(workflow.NewDataChannels(8))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	const goroutines = 8
	var wg sync.WaitGroup
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_ = s.AgentStatuses()
			}
		}()
	}
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_ = s.Reload(cfg.Repos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends)
			}
		}()
	}
	wg.Wait()
}

// TestSchedulerReloadReleasesMuBeforeSinkCall verifies that Reload releases
// bindMu before calling into the HotReloadSink. A blocked sink call must not
// stall AgentStatuses (the read path /agents and /status hit on every poll).
//
// Lock-ordering rationale: if the Engine sink ever held cfgMu.RLock while
// indirectly calling back into the scheduler (which would need bindMu.RLock),
// Reload holding bindMu.Lock during the sink call would form a cycle.
func TestSchedulerReloadReleasesMuBeforeSinkCall(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, map[string]ai.Runner{"claude": newStubRunner()}, newMapMemory(), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithRunnerBuilder(func(_ string, _ fleet.Backend) ai.Runner { return newStubRunner() })

	sinkEntered := make(chan struct{})
	sinkRelease := make(chan struct{})
	sink := &testHotReloadSink{
		updateConfigAndRunner: func(*config.Config, map[string]ai.Runner) {
			close(sinkEntered)
			<-sinkRelease
		},
	}
	s.WithHotReloadSink(sink)

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- s.Reload(cfg.Repos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends)
	}()

	<-sinkEntered

	statusesDone := make(chan struct{})
	go func() {
		_ = s.AgentStatuses()
		close(statusesDone)
	}()

	select {
	case <-statusesDone:
		// Good: bindMu was released before the sink call.
	case <-time.After(2 * time.Second):
		t.Error("AgentStatuses blocked while Reload was in the sink call — bindMu must be released before UpdateConfigAndRunners")
	}

	close(sinkRelease)
	if err := <-reloadDone; err != nil {
		t.Errorf("Reload: %v", err)
	}
}
