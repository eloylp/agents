package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

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
			s, err := NewScheduler(baseCfg(tc.mutate), zerolog.Nop())
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
	s, err := NewScheduler(baseCfg(nil), zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	q := workflow.NewDataChannels(4)
	s.WithEventQueue(q)

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
	s, err := NewScheduler(baseCfg(nil), zerolog.Nop())
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

func TestRebuildCron(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if got := len(s.AgentStatuses()); got != 1 {
		t.Fatalf("before rebuild: got %d statuses, want 1", got)
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
	if err := s.RebuildCron(newRepos, newAgents, cfg.Skills, map[string]fleet.Backend{"claude": {Command: "claude"}}); err != nil {
		t.Fatalf("RebuildCron: %v", err)
	}

	statuses := s.AgentStatuses()
	if len(statuses) != 1 {
		t.Fatalf("after rebuild: got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Name != "scanner" || statuses[0].Repo != "owner/other" {
		t.Errorf("after rebuild: got %s/%s, want scanner/owner/other", statuses[0].Name, statuses[0].Repo)
	}
}

// TestRebuildCronClearsAllBindings verifies that a rebuild with no cron
// bindings removes all previously registered entries.
func TestRebuildCronClearsAllBindings(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	repos := []fleet.Repo{{Name: "owner/repo", Enabled: true, Use: []fleet.Binding{
		{Agent: "reviewer", Labels: []string{"ai:fix"}},
	}}}
	if err := s.RebuildCron(repos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("RebuildCron: %v", err)
	}
	if got := len(s.AgentStatuses()); got != 0 {
		t.Errorf("after rebuild with no cron: got %d statuses, want 0", got)
	}
}

// TestRebuildCronRollsBackOnFailure verifies that a rebuild that cannot
// register new cron entries preserves the previous scheduler state.
func TestRebuildCronRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	before := s.AgentStatuses()
	if len(before) != 1 {
		t.Fatalf("before rebuild: got %d statuses, want 1", len(before))
	}

	badRepos := []fleet.Repo{{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "ghost", Cron: "* * * * *"}},
	}}
	badAgents := []fleet.Agent{}

	if err := s.RebuildCron(badRepos, badAgents, cfg.Skills, cfg.Daemon.AIBackends); err == nil {
		t.Fatal("RebuildCron: expected error for unknown agent binding, got nil")
	}

	after := s.AgentStatuses()
	if len(after) != len(before) {
		t.Errorf("after failed rebuild: got %d statuses, want %d (original preserved)",
			len(after), len(before))
	}
	if len(after) > 0 && after[0].Name != before[0].Name {
		t.Errorf("after failed rebuild: agent %q, want %q (original preserved)",
			after[0].Name, before[0].Name)
	}
	if len(s.cfg.Agents) != len(cfg.Agents) {
		t.Errorf("after failed rebuild: cfg.Agents len=%d, want %d (original preserved)",
			len(s.cfg.Agents), len(cfg.Agents))
	}
}

// TestRebuildCronCopyOnWrite verifies that RebuildCron does not mutate the
// caller's *config.Config pointer. Goroutines that hold a snapshot of the old
// pointer must keep seeing the pre-rebuild values; only future snapshots
// should see the new values.
func TestRebuildCronCopyOnWrite(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	originalPtr := cfg
	s, err := NewScheduler(cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	newRepos := []fleet.Repo{{Name: "owner/new-repo", Enabled: true, Use: []fleet.Binding{{Agent: "reviewer", Cron: "* * * * *"}}}}
	if err := s.RebuildCron(newRepos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("RebuildCron: %v", err)
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

// TestRebuildCronRaceWithConcurrentReads runs RebuildCron in parallel with
// the read paths the daemon hits concurrently — AgentStatuses (called by
// /agents, /status) and the cron closures fired by the cron library. Run
// with -race to catch concurrent map/struct accesses against the cfg
// snapshot.
func TestRebuildCronRaceWithConcurrentReads(t *testing.T) {
	t.Parallel()
	cfg := baseCfg(nil)
	s, err := NewScheduler(cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
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
				_ = s.RebuildCron(cfg.Repos, cfg.Agents, cfg.Skills, cfg.Daemon.AIBackends)
			}
		}()
	}
	wg.Wait()
}
