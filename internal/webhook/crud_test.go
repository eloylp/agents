package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// openCRUDTestServer creates a test server wired with an in-memory SQLite
// store.
func openCRUDTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := crudMinimalConfig()
	dc := workflow.NewDataChannels(1)
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, zerolog.Nop())
	s.WithStore(db, nil) // nil reloader — cron hot-reload not exercised here
	return s
}

func doCRUDRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	return rr
}

// crudMinimalConfig returns a minimal config suitable for CRUD tests.
func crudMinimalConfig() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{
				ListenAddr:          ":8080",
				StatusPath:          "/status",
				WebhookPath:         "/webhooks/github",
				WriteTimeoutSeconds: 15,
			},
		},
	}
}

// ── /api/store/agents ────────────────────────────────────────────────────────

func TestStoreCRUDAgentListEmpty(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodGet, "/api/store/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/store/agents: got %d, want 200", rr.Code)
	}
	var agents []any
	if err := json.NewDecoder(rr.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("got %d agents, want 0", len(agents))
	}
}

func TestStoreCRUDAgentCreateAndGet(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	payload := map[string]any{
		"name":           "coder",
		"backend":        "claude",
		"skills":         []string{"architect"},
		"prompt":         "You write code.",
		"allow_prs":      true,
		"allow_dispatch": true,
		"can_dispatch":   []string{"pr-reviewer"},
		"description":    "coding agent",
	}

	// POST — create
	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", payload)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/store/agents: got %d, want 200 — %s", rr.Code, rr.Body.String())
	}

	// GET list — should have one entry
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/store/agents: got %d", rr.Code)
	}
	var agents []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&agents); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("list: got %d, want 1", len(agents))
	}
	if agents[0]["name"] != "coder" {
		t.Errorf("name: got %v", agents[0]["name"])
	}

	// GET single
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/store/agents/coder: got %d", rr.Code)
	}
}

func TestStoreCRUDAgentDeleteNotFound(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodGet, "/api/store/agents/ghost", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET non-existent agent: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	payload := map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}
	doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", payload)

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/agents/coder", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE: got %d, want 204", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/agents/coder", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentMissingName(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{"backend": "claude"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST without name: got %d, want 400", rr.Code)
	}
}

// ── /api/store/skills ─────────────────────────────────────────────────────────

func TestStoreCRUDSkillCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/skills", map[string]any{
		"name": "architect", "prompt": "Focus on architecture.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST skill: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/skills/architect", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET skill: got %d", rr.Code)
	}
	var skill map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&skill); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if skill["prompt"] != "Focus on architecture." {
		t.Errorf("prompt: got %v", skill["prompt"])
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/api/store/skills/architect", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE skill: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/skills/architect", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// ── /api/store/backends ───────────────────────────────────────────────────────

func TestStoreCRUDBackendCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/backends", map[string]any{
		"name":            "claude",
		"command":         "claude",
		"args":            []string{"-p"},
		"env":             map[string]string{},
		"timeout_seconds": 300,
		"max_prompt_chars": 8000,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST backend: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET backend: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/api/store/backends/claude", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE backend: got %d", rr.Code)
	}
}

// ── /api/store/repos ──────────────────────────────────────────────────────────

func TestStoreCRUDRepoCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// First create the agent that the binding references.
	doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	})

	enabled := true
	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/repos", map[string]any{
		"name":    "owner/repo",
		"enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": enabled},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST repo: got %d — %s", rr.Code, rr.Body.String())
	}

	// GET list
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/repos", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repos: got %d", rr.Code)
	}
	var repos []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&repos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(repos))
	}

	// GET single — repo name is owner/repo → /api/store/repos/owner/repo
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/repos/owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repo: got %d", rr.Code)
	}
	var repo map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&repo); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if repo["name"] != "owner/repo" {
		t.Errorf("name: got %v", repo["name"])
	}

	// DELETE
	rr = doCRUDRequest(t, s, http.MethodDelete, "/api/store/repos/owner/repo", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE repo: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/repos/owner/repo", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// TestStoreCRUDNotAvailableWithoutStore verifies that /api/store/* routes are
// not registered when no SQLite store has been attached (YAML-only config).
func TestStoreCRUDNotAvailableWithoutStore(t *testing.T) {
	t.Parallel()
	cfg := crudMinimalConfig()
	dc := workflow.NewDataChannels(1)
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, zerolog.Nop())
	// No WithStore call — db is nil, routes not registered.

	for _, path := range []string{
		"/api/store/agents",
		"/api/store/skills",
		"/api/store/backends",
		"/api/store/repos",
	} {
		rr := doCRUDRequest(t, s, http.MethodGet, path, nil)
		if rr.Code != http.StatusNotFound {
			// Routes are not registered at all when db is nil.
			t.Errorf("GET %s without store: got %d, want 404", path, rr.Code)
		}
	}
}
