package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	serverfleet "github.com/eloylp/agents/internal/server/fleet"
)

func TestAgentsOrphansEndpointAndStatusSummary(t *testing.T) {
	t.Parallel()

	cfg := testCfg(func(c *config.Config) {
		c.Daemon.AIBackends = map[string]fleet.Backend{
			"claude": {
				Command: "claude",
				Models:  []string{"claude-4"},
			},
		}
		c.Agents = []fleet.Agent{
			{Name: "coder", Backend: "claude", Model: "claude-3.5", Prompt: "x"},
		}
	})
	srv, _ := newTestServer(cfg)
	h := srv.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents/orphans/status", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/orphans/status: got %d", rr.Code)
	}

	var snapshot serverfleet.OrphanedAgentsSnapshot
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
