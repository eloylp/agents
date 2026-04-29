// Package daemontest is the shared test fixture for *daemon.Daemon. It
// builds a Daemon backed by a tempdir SQLite, with the rest of the
// runtime components wired exactly the way production wires them — the
// same fixture pattern internal/coordinator and internal/mcp use.
//
// Using a real Daemon (rather than per-package stubs) keeps the tests
// honest: a CRUD write that triggers a reload exercises the real
// coordinator chain, the real engine's UpdateConfigAndRunners, the real
// scheduler's RebuildCron — same code paths production runs.
package daemontest

import (
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/daemon"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// New constructs a Daemon backed by a tempdir SQLite seeded from cfg.
// The DB and Daemon are torn down through t.Cleanup so callers don't have
// to. cfg is imported once at startup; the returned *daemon.Daemon holds
// the same cfg snapshot until a CRUD write triggers a reload.
//
// If cfg has no agents or backends, default ones are seeded so ImportAll's
// "at least one agent / backend" constraint passes — most webhook /status
// tests don't care which agents exist; they just need the daemon to boot.
func New(t *testing.T, cfg *config.Config) *daemon.Daemon {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	agents := cfg.Agents
	backends := cfg.Daemon.AIBackends
	if len(backends) == 0 {
		backends = map[string]fleet.Backend{
			"claude": {Command: "claude", TimeoutSeconds: 60, MaxPromptChars: 1024},
		}
		cfg.Daemon.AIBackends = backends
	}
	if len(agents) == 0 {
		agents = []fleet.Agent{{Name: "coder", Backend: pickAnyBackend(backends), Prompt: "code"}}
		cfg.Agents = agents
	}

	if err := store.ImportAll(
		db,
		agents,
		cfg.Repos,
		cfg.Skills,
		backends,
	); err != nil {
		t.Fatalf("import seed: %v", err)
	}

	d, err := daemon.New(cfg, db, zerolog.Nop())
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	return d
}

func pickAnyBackend(backends map[string]fleet.Backend) string {
	for name := range backends {
		return name
	}
	return "claude"
}

// MinimalCfg returns a *config.Config with the daemon-level fields the
// webhook + status tests rely on populated to sane defaults. Callers can
// mutate further via the optional mutator.
func MinimalCfg(mutator func(*config.Config)) *config.Config {
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{
				ListenAddr:             ":0",
				WebhookPath:            "/webhooks/github",
				StatusPath:             "/status",
				MaxBodyBytes:           1024,
				WebhookSecret:          "secret",
				DeliveryTTLSeconds:     3600,
				ShutdownTimeoutSeconds: 5,
			},
			Processor: config.ProcessorConfig{
				EventQueueBuffer:    16,
				MaxConcurrentAgents: 1,
				Dispatch: config.DispatchConfig{
					MaxDepth:            2,
					MaxFanout:           2,
					DedupWindowSeconds:  60,
				},
			},
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude", TimeoutSeconds: 60, MaxPromptChars: 1024},
			},
		},
		Repos: []fleet.Repo{{Name: "owner/repo", Enabled: true}},
	}
	if mutator != nil {
		mutator(cfg)
	}
	return cfg
}
