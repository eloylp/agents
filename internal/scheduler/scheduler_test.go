package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// drainQueue reads up to n events from a buffered DataChannels without
// blocking past the timeout.
func drainQueue(t *testing.T, dc *workflow.DataChannels, n int) []workflow.Event {
	t.Helper()
	out := make([]workflow.Event, 0, n)
	deadline := time.After(100 * time.Millisecond)
	for len(out) < n {
		select {
		case qe := <-dc.EventChan():
			out = append(out, qe.Event)
		case <-deadline:
			return out
		}
	}
	return out
}

// seedStore opens a tempdir SQLite, imports a minimal valid fleet, and
// returns the data-access store. The fixture has one cron-bound agent
// on owner/repo.
func seedStore(t *testing.T, repos []fleet.Repo) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })

	agents := []fleet.Agent{
		{Name: "reviewer", Backend: "claude", Skills: []string{"architect"}, Prompt: "Review PRs."},
	}
	skills := map[string]fleet.Skill{"architect": {Prompt: "Focus on architecture."}}
	backends := map[string]fleet.Backend{"claude": {Command: "claude"}}
	if err := st.ImportAll(agents, repos, skills, backends); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st
}

func defaultRepos() []fleet.Repo {
	return []fleet.Repo{{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "reviewer", Cron: "* * * * *"}},
	}}
}

// TestNewSchedulerEntryRegistration checks the initial reconcile that runs
// inside NewScheduler picks up the cron bindings from SQLite.
func TestNewSchedulerEntryRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		repos     []fleet.Repo
		wantCount int
	}{
		{
			name:      "cron binding registered",
			repos:     defaultRepos(),
			wantCount: 1,
		},
		{
			name: "skips disabled repo",
			repos: func() []fleet.Repo {
				r := defaultRepos()
				r[0].Enabled = false
				return r
			}(),
			wantCount: 0,
		},
		{
			name: "skips disabled binding",
			repos: func() []fleet.Repo {
				f := false
				r := defaultRepos()
				r[0].Use[0].Enabled = &f
				return r
			}(),
			wantCount: 0,
		},
		{
			name: "skips label-only binding",
			repos: []fleet.Repo{{
				Name:    "owner/repo",
				Enabled: true,
				Use:     []fleet.Binding{{Agent: "reviewer", Labels: []string{"ai:review"}}},
			}},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := seedStore(t, tc.repos)
			s, err := NewScheduler(st, time.Hour, zerolog.Nop())
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if got := len(s.agentEntries); got != tc.wantCount {
				t.Errorf("agentEntries = %d, want %d", got, tc.wantCount)
			}
		})
	}
}

// TestCronTickPushesEvent verifies the producer-mode contract: a fired
// cron entry pushes a "cron" event onto the queue.
func TestCronTickPushesEvent(t *testing.T) {
	t.Parallel()
	st := seedStore(t, defaultRepos())
	s, err := NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	q := workflow.NewDataChannels(4, st)
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

// TestRecordLastRunUpdatesAgentStatuses verifies the LastRunRecorder hook
// the engine calls when a cron run completes.
func TestRecordLastRunUpdatesAgentStatuses(t *testing.T) {
	t.Parallel()
	st := seedStore(t, defaultRepos())
	s, err := NewScheduler(st, time.Hour, zerolog.Nop())
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

// TestReconcilePicksUpAddedBinding verifies that a cron binding added to
// SQLite after the scheduler is constructed is registered on the next
// reconcile.
func TestReconcilePicksUpAddedBinding(t *testing.T) {
	t.Parallel()
	st := seedStore(t, []fleet.Repo{
		{Name: "owner/repo", Enabled: true, Use: []fleet.Binding{{Agent: "reviewer", Labels: []string{"ai:fix"}}}},
	})
	s, err := NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if got := len(s.agentEntries); got != 0 {
		t.Fatalf("initial agentEntries = %d, want 0", got)
	}

	// Replace the binding with a cron one and reconcile.
	if err := st.UpsertRepo(fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "reviewer", Cron: "* * * * *"}},
	}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := s.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(s.agentEntries); got != 1 {
		t.Fatalf("after reconcile: agentEntries = %d, want 1", got)
	}
}

// TestReconcileRemovesStaleBinding verifies that a cron binding removed
// from SQLite is unregistered from cron on the next reconcile.
func TestReconcileRemovesStaleBinding(t *testing.T) {
	t.Parallel()
	st := seedStore(t, defaultRepos())
	s, err := NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if got := len(s.agentEntries); got != 1 {
		t.Fatalf("initial agentEntries = %d, want 1", got)
	}

	if err := st.UpsertRepo(fleet.Repo{
		Name:    "owner/repo",
		Enabled: true,
		Use:     []fleet.Binding{{Agent: "reviewer", Labels: []string{"ai:fix"}}},
	}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := s.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := len(s.agentEntries); got != 0 {
		t.Fatalf("after reconcile: agentEntries = %d, want 0", got)
	}
}

// TestReconcileRaceWithConcurrentReads runs reconcile concurrently with
// AgentStatuses to catch races. Run with -race.
func TestReconcileRaceWithConcurrentReads(t *testing.T) {
	t.Parallel()
	st := seedStore(t, defaultRepos())
	s, err := NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	s.WithEventQueue(workflow.NewDataChannels(8, st))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
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
				_ = s.Reconcile()
			}
		}()
	}
	wg.Wait()
}
