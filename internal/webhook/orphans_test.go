package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/eloylp/agents/internal/config"
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

	cfg := testCfg(func(c *config.Config) {
		c.Daemon.AIBackends = map[string]config.AIBackendConfig{
			"claude": {
				Command: "claude",
				Models:  []string{"claude-3.7", "claude-4"},
			},
		}
		c.Agents = []config.AgentDef{
			{Name: "coder", Backend: "claude", Model: "claude-3.5", Prompt: "x"},
			{Name: "reviewer", Backend: "claude", Model: "claude-4", Prompt: "x"},
			{Name: "defaulted", Backend: "claude", Prompt: "x"},
		}
		enabled := true
		c.Repos = []config.RepoDef{
			{
				Name:    "owner/repo",
				Enabled: true,
				Use: []config.Binding{
					{Agent: "coder", Labels: []string{"ai:fix"}, Enabled: &enabled},
				},
			},
		}
	})

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

func TestAgentsOrphansEndpointAndStatusSummary(t *testing.T) {
	t.Parallel()

	cfg := testCfg(func(c *config.Config) {
		c.Daemon.AIBackends = map[string]config.AIBackendConfig{
			"claude": {
				Command: "claude",
				Models:  []string{"claude-4"},
			},
		}
		c.Agents = []config.AgentDef{
			{Name: "coder", Backend: "claude", Model: "claude-3.5", Prompt: "x"},
		}
	})
	srv, _ := newTestServer(cfg)
	h := srv.buildHandler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents/orphans/status", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/orphans/status: got %d", rr.Code)
	}

	var snapshot OrphanedAgentsSnapshot
	if err := json.NewDecoder(rr.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode /agents/orphans/status: %v", err)
	}
	if snapshot.Count != 1 || len(snapshot.Agents) != 1 {
		t.Fatalf("orphan snapshot count=%d agents=%d, want 1/1", snapshot.Count, len(snapshot.Agents))
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /status: got %d", rr.Code)
	}
	var status struct {
		OrphanedAgents struct {
			Count int `json:"count"`
		} `json:"orphaned_agents"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	if status.OrphanedAgents.Count != 1 {
		t.Fatalf("status orphaned_agents.count = %d, want 1", status.OrphanedAgents.Count)
	}
}
