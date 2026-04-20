package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
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
				MaxBodyBytes:        1 << 20, // 1 MiB
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

// ── reloadCron failure ────────────────────────────────────────────────────────

// errCronReloader satisfies CronReloader and always returns an error from Reload.
type errCronReloader struct{ err error }

func (r *errCronReloader) Reload([]config.RepoDef, []config.AgentDef, map[string]config.SkillDef, map[string]config.AIBackendConfig) error {
	return r.err
}

// openCRUDTestServerWithReloader creates a test server wired with a SQLite
// store and the given CronReloader.
func openCRUDTestServerWithReloader(t *testing.T, reloader CronReloader) *Server {
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
	s.WithStore(db, reloader)
	return s
}

// TestStoreCRUDReloadFailureReturns500 verifies that when reloadCron fails
// (e.g. the scheduler can't re-register a cron binding), write endpoints
// return 500 instead of silently acknowledging a partially-applied change.
func TestStoreCRUDReloadFailureReturns500(t *testing.T) {
	t.Parallel()

	reloadErr := errors.New("scheduler broken")
	reloader := &errCronReloader{err: reloadErr}

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "POST agent",
			method: http.MethodPost,
			path:   "/api/store/agents",
			body:   map[string]any{"name": "agent-x", "backend": "claude", "prompt": "x"},
		},
		{
			name:   "DELETE agent",
			method: http.MethodDelete,
			path:   "/api/store/agents/agent-x",
			body:   nil,
		},
		{
			name:   "POST repo",
			method: http.MethodPost,
			path:   "/api/store/repos",
			body:   map[string]any{"name": "owner/repo", "enabled": true},
		},
		{
			name:   "DELETE repo",
			method: http.MethodDelete,
			path:   "/api/store/repos/owner/repo",
			body:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openCRUDTestServerWithReloader(t, reloader)
			rr := doCRUDRequest(t, s, tc.method, tc.path, tc.body)
			if rr.Code != http.StatusInternalServerError {
				t.Errorf("%s %s: want 500 on reload failure, got %d: %s",
					tc.method, tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

// ── /api/store POST body-size limiting ───────────────────────────────────────

// TestStoreCRUDPostBodySizeLimit verifies that POST write endpoints reject
// request bodies that exceed daemon.http.max_body_bytes.
func TestStoreCRUDPostBodySizeLimit(t *testing.T) {
	t.Parallel()

	cfg := crudMinimalConfig()
	cfg.Daemon.HTTP.MaxBodyBytes = 10 // very small limit for the test

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dc := workflow.NewDataChannels(1)
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, zerolog.Nop())
	s.WithStore(db, nil)

	tests := []struct {
		name string
		path string
		body any
	}{
		{
			name: "agent",
			path: "/api/store/agents",
			body: map[string]any{"name": "coder", "backend": "claude", "prompt": "You write code — a much longer prompt than 10 bytes."},
		},
		{
			name: "skill",
			path: "/api/store/skills",
			body: map[string]any{"name": "arch", "prompt": "You are an architect — longer than 10 bytes."},
		},
		{
			name: "backend",
			path: "/api/store/backends",
			body: map[string]any{"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{}},
		},
		{
			name: "repo",
			path: "/api/store/repos",
			body: map[string]any{"name": "owner/repo", "enabled": true, "bindings": []any{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := doCRUDRequest(t, s, http.MethodPost, tc.path, tc.body)
			if rr.Code == http.StatusOK {
				t.Errorf("POST %s: expected non-200 for oversized body, got 200", tc.path)
			}
		})
	}
}

// ── /api/store write-endpoint authentication ──────────────────────────────────

// openCRUDTestServerWithAPIKey creates a test server with an API key configured.
func openCRUDTestServerWithAPIKey(t *testing.T, apiKey string) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := crudMinimalConfig()
	cfg.Daemon.HTTP.APIKey = apiKey
	dc := workflow.NewDataChannels(1)
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, zerolog.Nop())
	s.WithStore(db, nil)
	return s
}

// doCRUDRequestWithToken sends a request with a Bearer token.
func doCRUDRequestWithToken(t *testing.T, s *Server, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	return rr
}

// TestStoreCRUDWriteEndpointsRequireAPIKey verifies that POST and DELETE
// endpoints return 401 when an API key is configured but not supplied, and
// that GET endpoints remain open without a token.
func TestStoreCRUDWriteEndpointsRequireAPIKey(t *testing.T) {
	t.Parallel()
	const key = "secret-token"
	s := openCRUDTestServerWithAPIKey(t, key)

	writeMethods := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/store/agents", map[string]any{"name": "x", "backend": "claude", "prompt": "p"}},
		{http.MethodDelete, "/api/store/agents/x", nil},
		{http.MethodPost, "/api/store/skills", map[string]any{"name": "s", "prompt": "p"}},
		{http.MethodDelete, "/api/store/skills/s", nil},
		{http.MethodPost, "/api/store/backends", map[string]any{"name": "b", "command": "c"}},
		{http.MethodDelete, "/api/store/backends/b", nil},
		{http.MethodPost, "/api/store/repos", map[string]any{"name": "owner/repo"}},
		{http.MethodDelete, "/api/store/repos/owner/repo", nil},
	}

	for _, tc := range writeMethods {
		t.Run(tc.method+" "+tc.path+" no token", func(t *testing.T) {
			t.Parallel()
			rr := doCRUDRequestWithToken(t, s, tc.method, tc.path, tc.body, "")
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: want 401 without token, got %d", tc.method, tc.path, rr.Code)
			}
		})
		t.Run(tc.method+" "+tc.path+" wrong token", func(t *testing.T) {
			t.Parallel()
			rr := doCRUDRequestWithToken(t, s, tc.method, tc.path, tc.body, "wrong")
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: want 401 with wrong token, got %d", tc.method, tc.path, rr.Code)
			}
		})
	}

	// GET endpoints must remain open (no token required).
	for _, path := range []string{
		"/api/store/agents",
		"/api/store/skills",
		"/api/store/backends",
		"/api/store/repos",
	} {
		t.Run("GET "+path+" no token", func(t *testing.T) {
			t.Parallel()
			rr := doCRUDRequestWithToken(t, s, http.MethodGet, path, nil, "")
			if rr.Code != http.StatusOK {
				t.Errorf("GET %s: want 200 without token, got %d", path, rr.Code)
			}
		})
	}
}
