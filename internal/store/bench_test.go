package store_test

import (
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

type fixtureSize struct{ agents, repos, bindings, skills, backends int }

// Two reference fleets: "solo" mirrors what a single-developer self-hosted
// install typically runs; "large" is well past the size most operators
// reach. Both numbers shipped together so we know how cost scales.
var (
	soloFleet  = fixtureSize{agents: 5, repos: 2, bindings: 2, skills: 4, backends: 2}
	largeFleet = fixtureSize{agents: 50, repos: 30, bindings: 4, skills: 20, backends: 5}
)

func benchSeed(b *testing.B, fs fixtureSize) string {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "bench.db")
	db, err := store.Open(dbPath)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Use names the validator accepts; "claude_local-N" parses as a custom
	// local backend when LocalModelURL is set.
	backendNames := []string{"claude", "codex", "claude_local", "local-a", "local-b"}
	if fs.backends > len(backendNames) {
		b.Fatalf("backends > %d", len(backendNames))
	}
	backends := map[string]fleet.Backend{}
	for i := range fs.backends {
		name := backendNames[i]
		bk := fleet.Backend{
			Command: name, TimeoutSeconds: 600, MaxPromptChars: 12000,
			Models: []string{"model-a", "model-b"},
		}
		if i >= 2 {
			bk.LocalModelURL = "http://127.0.0.1:1234"
		}
		backends[name] = bk
	}
	primaryBackend := backendNames[0]
	skills := map[string]fleet.Skill{}
	for i := range fs.skills {
		skills["skill-"+strconv.Itoa(i)] = fleet.Skill{Prompt: "guidance number " + strconv.Itoa(i)}
	}
	agents := make([]fleet.Agent, 0, fs.agents)
	for i := range fs.agents {
		name := "agent-" + strconv.Itoa(i)
		agents = append(agents, fleet.Agent{
			Name: name, Backend: primaryBackend, Prompt: "do work " + strconv.Itoa(i),
			Description: "agent number " + strconv.Itoa(i),
		})
	}
	repos := make([]fleet.Repo, 0, fs.repos)
	for i := range fs.repos {
		repoName := "owner/repo-" + strconv.Itoa(i)
		bindings := make([]fleet.Binding, 0, fs.bindings)
		for j := range fs.bindings {
			bindings = append(bindings, fleet.Binding{
				Agent:  "agent-" + strconv.Itoa((i+j)%fs.agents),
				Labels: []string{"ai:label-" + strconv.Itoa(j)},
			})
		}
		repos = append(repos, fleet.Repo{Name: repoName, Enabled: true, Use: bindings})
	}

	if err := store.ImportAll(db, agents, repos, skills, backends); err != nil {
		b.Fatalf("seed: %v", err)
	}
	return dbPath
}

// BenchmarkReadSnapshot measures the cost of a full SQLite read of every
// entity (agents, repos with bindings, skills, backends). This is what the
// coordinator runs on every CRUD-triggered reload, and what we'd run on
// every Config() call if we eliminated the in-memory cache.
func BenchmarkReadSnapshot(b *testing.B) {
	for _, tc := range []struct {
		name string
		fs   fixtureSize
	}{
		{"solo", soloFleet},
		{"large", largeFleet},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dbPath := benchSeed(b, tc.fs)
			db, err := store.Open(dbPath)
			if err != nil {
				b.Fatalf("reopen: %v", err)
			}
			defer db.Close()

			b.ResetTimer()
			for range b.N {
				if _, _, _, _, err := store.ReadSnapshot(db); err != nil {
					b.Fatalf("ReadSnapshot: %v", err)
				}
			}
		})
	}
}

// BenchmarkReadSnapshotParallel measures concurrent readers (mirrors the
// daemon under load: many concurrent webhook handlers each calling
// Config()).
func BenchmarkReadSnapshotParallel(b *testing.B) {
	for _, tc := range []struct {
		name string
		fs   fixtureSize
	}{
		{"solo", soloFleet},
		{"large", largeFleet},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dbPath := benchSeed(b, tc.fs)
			db, err := store.Open(dbPath)
			if err != nil {
				b.Fatalf("reopen: %v", err)
			}
			defer db.Close()

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, _, _, _, err := store.ReadSnapshot(db); err != nil {
						b.Fatalf("ReadSnapshot: %v", err)
					}
				}
			})
		})
	}
}

// BenchmarkPointerRead measures the cost of the current in-memory cfg
// snapshot — what coord.Config() does today: an RWMutex.RLock, a pointer
// load, an unlock. The number is the baseline we'd be giving up if we
// went database-on-every-read.
func BenchmarkPointerRead(b *testing.B) {
	var p atomic.Pointer[int]
	x := 42
	p.Store(&x)
	b.ResetTimer()
	for range b.N {
		_ = p.Load()
	}
}
