package workflow

import (
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func testLogger() zerolog.Logger { return zerolog.Nop() }

// newTempStore opens a fresh tempdir SQLite and returns the data-access
// store. Used by tests that need a Store for DataChannels but don't need
// any seeded entities.
func newTempStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	return st
}

// seedStoreFromCfg opens a tempdir SQLite, imports the four entity sets
// from cfg, and returns the data-access store. Tests build their
// *config.Config the way they always have and hand it here to
// materialise the DB the engine reads from.
func seedStoreFromCfg(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	if err := st.ImportAll(cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends, cfg.Guardrails, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st
}

// newEngineFromCfg builds an Engine that reads from a tempdir SQLite
// seeded from cfg, with a stub runner that the test can configure.
// Replaces the pre-cutover NewEngine(cfg, runners, ...) shape used
// throughout the workflow test files.
func newEngineFromCfg(t *testing.T, cfg *config.Config, runners map[string]ai.Runner, queue EventEnqueuer) *Engine {
	t.Helper()
	st := seedStoreFromCfg(t, cfg)
	e := NewEngine(st, cfg.Daemon.Processor, queue, testLogger())
	if len(runners) > 0 {
		e.WithRunnerBuilder(func(name string, _ fleet.Backend) ai.Runner {
			if r, ok := runners[name]; ok {
				return r
			}
			return runners["claude"] // fallback for tests that only register one stub
		})
	}
	return e
}

// updateRuntimeConfig writes the new entity sets to the engine's store
// so the next event-handling pass picks them up. Replaces the
// pre-cutover e.UpdateConfigAndRunners hot-reload call site for tests
// that exercise config changes mid-test.
func updateRuntimeConfig(t *testing.T, e *Engine, cfg *config.Config, runners map[string]ai.Runner) {
	t.Helper()
	if err := e.store.ReplaceAll(cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends, cfg.Guardrails, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(runners) > 0 {
		e.WithRunnerBuilder(func(name string, _ fleet.Backend) ai.Runner {
			if r, ok := runners[name]; ok {
				return r
			}
			for _, r := range runners {
				return r
			}
			return nil
		})
	}
}
