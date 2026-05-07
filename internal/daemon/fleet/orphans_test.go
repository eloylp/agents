package fleet

import (
	"slices"
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func TestCanonicalModels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"nil input", nil, nil},
		{"empty slice", []string{}, nil},
		{"all empty after trim", []string{"", "  ", "\t"}, nil},
		{"dedup and sort", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"trims whitespace", []string{"  beta  ", "alpha", "beta"}, []string{"alpha", "beta"}},
		{"single", []string{"only"}, []string{"only"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalModels(tc.input)
			if !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("canonicalModels(%v) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

func TestComputeOrphanedAgents(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]fleet.Backend{
				"claude": {
					Command: "claude",
					Models:  []string{"claude-3.7", "claude-4"},
				},
			},
		},
		Agents: []fleet.Agent{
			{Name: "coder", Backend: "claude", Model: "claude-3.5", Prompt: "x"},
			{Name: "reviewer", Backend: "claude", Model: "claude-4", Prompt: "x"},
			{Name: "defaulted", Backend: "claude", Prompt: "x"},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "coder", Labels: []string{"ai:fix"}},
				},
			},
		},
	}

	orphans := computeOrphanedAgents(cfg)
	if len(orphans) != 1 {
		t.Fatalf("len(orphans) = %d, want 1", len(orphans))
	}
	if orphans[0].Name != "coder" {
		t.Fatalf("orphan name = %q, want %q", orphans[0].Name, "coder")
	}
	if orphans[0].Backend != "claude" {
		t.Fatalf("orphan backend = %q, want %q", orphans[0].Backend, "claude")
	}
	if len(orphans[0].AvailableModels) != 2 {
		t.Fatalf("available_models len = %d, want 2", len(orphans[0].AvailableModels))
	}
	if len(orphans[0].Repos) != 1 || orphans[0].Repos[0] != "owner/repo" {
		t.Fatalf("repos = %v, want [owner/repo]", orphans[0].Repos)
	}
}

// TestComputeOrphanedAgentsIncludesDisabledRefs verifies the Repos field on
// each orphan reflects every repo that references the agent, including
// disabled-repo and disabled-binding references. The orphan badge fires on
// "model not in backend catalog", runtime reachability is a separate
// concern. Hiding disabled references made the orphan report inaccurate
// when a repo was paused but its bindings still need fixing or
// re-pointing before re-enabling.
func TestComputeOrphanedAgentsIncludesDisabledRefs(t *testing.T) {
	t.Parallel()

	disabled := false
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]fleet.Backend{
				"claude": {
					Command: "claude",
					Models:  []string{"claude-3.7", "claude-4"},
				},
			},
		},
		Agents: []fleet.Agent{
			{Name: "coder", Backend: "claude", Model: "claude-3.5", Prompt: "x"},
		},
		Repos: []fleet.Repo{
			{
				Name:    "owner/active",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "coder", Labels: []string{"ai:fix"}},
				},
			},
			{
				Name:    "owner/paused",
				Enabled: false, // whole repo paused
				Use: []fleet.Binding{
					{Agent: "coder", Labels: []string{"ai:fix"}},
				},
			},
			{
				Name:    "owner/halfway",
				Enabled: true,
				Use: []fleet.Binding{
					{Agent: "coder", Cron: "0 * * * *", Enabled: &disabled}, // binding off
				},
			},
		},
	}

	orphans := computeOrphanedAgents(cfg)
	if len(orphans) != 1 {
		t.Fatalf("len(orphans) = %d, want 1", len(orphans))
	}
	wantRepos := []string{"owner/active", "owner/halfway", "owner/paused"}
	if !slices.Equal(orphans[0].Repos, wantRepos) {
		t.Fatalf("repos = %v, want %v (all references including disabled)", orphans[0].Repos, wantRepos)
	}
}
