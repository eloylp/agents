package coordinator

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// fixtureCfg is a minimal *config.Config sufficient for the coordinator's
// reload chain — the store-snapshot read returns whatever the seed put in
// SQLite, so the cfg here just supplies the daemon-level fields the merge
// preserves.
func fixtureCfg() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{ListenAddr: ":0"},
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude"},
			},
		},
	}
}

// setup builds a Coordinator against a temp-file SQLite seeded with one
// agent / repo / skill / backend, plus a custom reload + post-reload hook
// the caller controls.
func setup(t *testing.T, reload ReloadFunc, onPost PostReloadFunc) *Coordinator {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.ImportAll(
		db,
		[]fleet.Agent{{Name: "coder", Backend: "claude", Prompt: "code"}},
		[]fleet.Repo{{Name: "owner/repo", Enabled: true, Use: []fleet.Binding{{Agent: "coder", Labels: []string{"bug"}}}}},
		map[string]fleet.Skill{"testing": {Prompt: "write good tests"}},
		map[string]fleet.Backend{"claude": {Command: "claude"}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return New(fixtureCfg(), db, reload, onPost)
}

// TestDoRunsFnUnderLockAndPropagatesReload exercises the happy path: fn
// runs, then the reload chain fires with a merged cfg carrying the seeded
// rows, then the post-reload hook fires.
func TestDoRunsFnUnderLockAndPropagatesReload(t *testing.T) {
	t.Parallel()

	var (
		fnCalls       atomic.Int32
		reloadCalls   atomic.Int32
		postCalls     atomic.Int32
		seenAgents    int
		seenBackends  int
		seenSkills    int
		seenRepos     int
		mu            sync.Mutex
		postCfgRepoCt int
	)

	reload := func(cfg *config.Config) error {
		reloadCalls.Add(1)
		mu.Lock()
		seenAgents = len(cfg.Agents)
		seenRepos = len(cfg.Repos)
		seenSkills = len(cfg.Skills)
		seenBackends = len(cfg.Daemon.AIBackends)
		mu.Unlock()
		return nil
	}
	post := func(cfg *config.Config) {
		postCalls.Add(1)
		mu.Lock()
		postCfgRepoCt = len(cfg.Repos)
		mu.Unlock()
	}
	c := setup(t, reload, post)

	if err := c.Do(func() error { fnCalls.Add(1); return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if fnCalls.Load() != 1 {
		t.Errorf("fn calls = %d, want 1", fnCalls.Load())
	}
	if reloadCalls.Load() != 1 {
		t.Errorf("reload calls = %d, want 1", reloadCalls.Load())
	}
	if postCalls.Load() != 1 {
		t.Errorf("post calls = %d, want 1", postCalls.Load())
	}
	if seenAgents != 1 || seenRepos != 1 || seenSkills != 1 || seenBackends != 1 {
		t.Errorf("merged cfg counts = (%d agents, %d repos, %d skills, %d backends), want all 1",
			seenAgents, seenRepos, seenSkills, seenBackends)
	}
	if postCfgRepoCt != 1 {
		t.Errorf("post cfg repo count = %d, want 1", postCfgRepoCt)
	}
	if got := len(c.Config().Repos); got != 1 {
		t.Errorf("Config().Repos after reload = %d, want 1", got)
	}
}

// TestDoSkipsReloadOnFnError verifies the lock-and-reload contract: if fn
// fails, the reload chain does NOT run (fn's error is the only outcome),
// and the cfg pointer remains unchanged.
func TestDoSkipsReloadOnFnError(t *testing.T) {
	t.Parallel()

	var reloadCalls atomic.Int32
	c := setup(t, func(*config.Config) error { reloadCalls.Add(1); return nil }, nil)
	beforeCfg := c.Config()

	want := errors.New("write failed")
	if err := c.Do(func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("Do err = %v, want %v", err, want)
	}
	if reloadCalls.Load() != 0 {
		t.Errorf("reload should not run when fn fails, got %d calls", reloadCalls.Load())
	}
	if c.Config() != beforeCfg {
		t.Error("cfg pointer changed despite fn failure")
	}
}

// TestDoSurfacesReloadError verifies that a ReloadFunc returning a non-nil
// error replaces the (nil) fn outcome — so the caller sees the reload
// failure that actually prevented the post-write coherence step.
func TestDoSurfacesReloadError(t *testing.T) {
	t.Parallel()

	want := errors.New("engine update failed")
	c := setup(t, func(*config.Config) error { return want }, nil)

	err := c.Do(func() error { return nil })
	if !errors.Is(err, want) {
		t.Fatalf("Do err = %v, want %v", err, want)
	}
}

// TestDoSerialisesConcurrentWrites verifies the daemon-wide write epoch:
// concurrent Do calls execute their fns sequentially under storeMu, so two
// writers cannot interleave their fn-then-reload sequences.
func TestDoSerialisesConcurrentWrites(t *testing.T) {
	t.Parallel()

	c := setup(t, nil, nil)

	// Each fn flips the active flag, sleeps briefly, asserts no-one else is
	// active, then unflips. Any overlap (== broken serialisation) trips the
	// race detector and/or surfaces an explicit error.
	var (
		active   atomic.Int32
		overlaps atomic.Int32
	)

	const goroutines = 16
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Do(func() error {
				if active.Add(1) != 1 {
					overlaps.Add(1)
				}
				active.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()

	if got := overlaps.Load(); got != 0 {
		t.Errorf("observed %d overlaps; storeMu should serialise Do calls", got)
	}
}

// TestNilCallbacks verifies that a Coordinator with nil reload + post hooks
// is usable — Do still acquires the lock and runs fn; the reload chain
// short-circuits.
func TestNilCallbacks(t *testing.T) {
	t.Parallel()

	c := setup(t, nil, nil)
	if err := c.Do(func() error { return nil }); err != nil {
		t.Fatalf("Do with nil callbacks: %v", err)
	}
}
