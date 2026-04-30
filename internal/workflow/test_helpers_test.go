package workflow

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func testLogger() zerolog.Logger { return zerolog.Nop() }

// seedDBFromCfg opens a tempdir SQLite, imports the four entity sets from
// cfg, and returns the live handle. Tests build their *config.Config the
// way they always have and hand it here to materialise the DB the engine
// reads from.
func seedDBFromCfg(t *testing.T, cfg *config.Config) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.ImportAll(db, cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

// newEngineFromCfg builds an Engine that reads from a tempdir SQLite seeded
// from cfg, with a stub runner that the test can configure. Replaces the
// pre-cutover NewEngine(cfg, runners, ...) shape used throughout the
// workflow test files.
func newEngineFromCfg(t *testing.T, cfg *config.Config, runners map[string]ai.Runner, queue EventEnqueuer) *Engine {
	t.Helper()
	db := seedDBFromCfg(t, cfg)
	logger := testLogger()
	e := NewEngine(db, cfg.Daemon.Processor, queue, logger)
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

// updateRuntimeConfig writes the new entity sets to the engine's DB so the
// next event-handling pass picks them up. Replaces the pre-cutover
// e.UpdateConfigAndRunners hot-reload call site for tests that exercise
// config changes mid-test.
func updateRuntimeConfig(t *testing.T, e *Engine, cfg *config.Config, runners map[string]ai.Runner) {
	t.Helper()
	if err := store.ReplaceAll(e.db, cfg.Agents, cfg.Repos, cfg.Skills, cfg.Daemon.AIBackends); err != nil {
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
