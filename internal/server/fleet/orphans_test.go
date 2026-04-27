package fleet

import (
	"reflect"
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
			if !reflect.DeepEqual(got, tc.want) {
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
