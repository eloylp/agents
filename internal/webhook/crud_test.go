package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// seedStoreBackend inserts a minimal backend into the server's store directly
// so that subsequent agent upserts that reference it pass cross-ref validation.
func seedStoreBackend(t *testing.T, s *Server, name string) {
	t.Helper()
	b := config.AIBackendConfig{Command: name, Args: []string{}, Env: map[string]string{}}
	if err := store.UpsertBackend(s.db, name, b); err != nil {
		t.Fatalf("seedStoreBackend %s: %v", name, err)
	}
}

// seedStoreSkill inserts a minimal skill into the server's store directly.
func seedStoreSkill(t *testing.T, s *Server, name string) {
	t.Helper()
	if err := store.UpsertSkill(s.db, name, config.SkillDef{Prompt: "skill prompt"}); err != nil {
		t.Fatalf("seedStoreSkill %s: %v", name, err)
	}
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

	seedStoreBackend(t, s, "claude")
	seedStoreSkill(t, s, "architect")
	// "pr-reviewer" must exist for can_dispatch validation.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "pr-reviewer", "backend": "claude", "prompt": "review code",
		"description": "a reviewer agent", "skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed pr-reviewer agent: got %d — %s", rr.Code, rr.Body.String())
	}

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

	// GET list — should have two entries: pr-reviewer (seeded) + coder.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/store/agents: got %d", rr.Code)
	}
	var agents []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&agents); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var found bool
	for _, a := range agents {
		if a["name"] == "coder" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("coder not found in agent list: %v", agents)
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

	seedStoreBackend(t, s, "claude")
	// Seed two agents so that deleting one still leaves the system valid.
	for _, name := range []string{"coder", "reviewer"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
			"name": name, "backend": "claude", "prompt": "p",
			"skills": []string{}, "can_dispatch": []string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("seed agent %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

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

	// Create two backends so that deleting one still leaves the system valid.
	for _, name := range []string{"claude", "codex"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/backends", map[string]any{
			"name": name, "command": name, "args": []string{}, "env": map[string]string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("POST backend %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/api/store/backends/claude", nil)
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

	seedStoreBackend(t, s, "claude")
	// First create the agent that the binding references.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed coder agent: got %d — %s", rr.Code, rr.Body.String())
	}

	// Create two repos so that deleting one still leaves the system valid.
	enabled := true
	for _, name := range []string{"owner/repo", "owner/other"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/repos", map[string]any{
			"name":    name,
			"enabled": true,
			"bindings": []map[string]any{
				{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": enabled},
			},
		}); rr.Code != http.StatusOK {
			t.Fatalf("POST repo %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

	// GET list
	rr := doCRUDRequest(t, s, http.MethodGet, "/api/store/repos", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repos: got %d", rr.Code)
	}
	var repos []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&repos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
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
		setup  func(t *testing.T, s *Server)
	}{
		{
			name:   "POST agent",
			method: http.MethodPost,
			path:   "/api/store/agents",
			body:   map[string]any{"name": "agent-x", "backend": "claude", "prompt": "x"},
			// Seed the backend so cross-ref validation passes and the test
			// genuinely exercises reload failure, not validation failure.
			setup: func(t *testing.T, s *Server) { seedStoreBackend(t, s, "claude") },
		},
		{
			name:   "DELETE agent",
			method: http.MethodDelete,
			path:   "/api/store/agents/agent-x",
			body:   nil,
		},
		{
			name:   "POST skill",
			method: http.MethodPost,
			path:   "/api/store/skills",
			body:   map[string]any{"name": "arch", "prompt": "Focus on architecture."},
		},
		{
			name:   "DELETE skill",
			method: http.MethodDelete,
			path:   "/api/store/skills/arch",
			body:   nil,
		},
		{
			name:   "POST backend",
			method: http.MethodPost,
			path:   "/api/store/backends",
			body:   map[string]any{"name": "claude2", "command": "claude2", "args": []string{}, "env": map[string]string{}},
			// Seed claude so there is already one backend, making the new backend
			// a valid addition (fleet validation requires at least one).
			setup: func(t *testing.T, s *Server) { seedStoreBackend(t, s, "claude") },
		},
		{
			name:   "DELETE backend",
			method: http.MethodDelete,
			path:   "/api/store/backends/claude",
			body:   nil,
			// Seed two backends so deleting one still leaves the fleet valid;
			// the reload failure is the only reason the handler should return 500.
			setup: func(t *testing.T, s *Server) {
				seedStoreBackend(t, s, "claude")
				seedStoreBackend(t, s, "codex")
			},
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
			if tc.setup != nil {
				tc.setup(t, s)
			}
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

// ── Concurrent write+reload serialization ────────────────────────────────────

// countingCronReloader records how many times Reload was called and captures
// the last snapshot it received. Safe for concurrent use.
type countingCronReloader struct {
	mu    sync.Mutex
	calls int32
	last  []config.AgentDef
}

func (r *countingCronReloader) Reload(_ []config.RepoDef, agents []config.AgentDef, _ map[string]config.SkillDef, _ map[string]config.AIBackendConfig) error {
	atomic.AddInt32(&r.calls, 1)
	r.mu.Lock()
	r.last = agents
	r.mu.Unlock()
	return nil
}

// TestConcurrentWriteReloadSerialisation verifies that concurrent POST
// /api/store/agents requests do not interleave their DB-write and in-memory
// Reload calls. Specifically, the last Reload that runs must see all agents
// that were successfully committed to SQLite — it must never reflect a stale
// snapshot from a request that finished earlier but whose Reload won the race.
//
// Running with -race also detects any data race on the reloader or storeMu.
func TestConcurrentWriteReloadSerialisation(t *testing.T) {
	t.Parallel()

	reloader := &countingCronReloader{}
	s := openCRUDTestServerWithReloader(t, reloader)

	seedStoreBackend(t, s, "claude")

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		name := "agent-concurrent-" + string(rune('a'+i))
		go func(agentName string) {
			defer wg.Done()
			rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents",
				map[string]any{"name": agentName, "backend": "claude", "prompt": "p"})
			if rr.Code != http.StatusOK {
				t.Errorf("POST agent %s: want 200, got %d: %s", agentName, rr.Code, rr.Body.String())
			}
		}(name)
	}
	wg.Wait()

	// Every request must have triggered exactly one reload.
	if got := atomic.LoadInt32(&reloader.calls); got != n {
		t.Errorf("expected %d Reload calls, got %d", n, got)
	}

	// The last recorded snapshot must include all n agents (monotonic guarantee:
	// the final Reload saw the DB state that includes every committed write).
	agents, err := store.ReadAgents(s.db)
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	if len(agents) != n {
		t.Fatalf("expected %d agents in DB, got %d", n, len(agents))
	}
	reloader.mu.Lock()
	lastCount := len(reloader.last)
	reloader.mu.Unlock()
	if lastCount != n {
		t.Errorf("last Reload snapshot had %d agents, expected %d (stale snapshot overwrote a newer one)", lastCount, n)
	}
}

// ── Cross-ref validation ──────────────────────────────────────────────────────

// TestStoreCRUDAgentRejectedWithUnknownBackend verifies that creating an agent
// that references a backend not present in the store is rejected.
func TestStoreCRUDAgentRejectedWithUnknownBackend(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// No backend seeded — "claude" is unknown.
	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	})
	if rr.Code == http.StatusOK {
		t.Errorf("POST agent with unknown backend: want non-200, got 200")
	}
}

// TestStoreCRUDDeleteBackendRejectedWhenReferenced verifies that deleting a
// backend still referenced by an agent is rejected and leaves the backend intact.
func TestStoreCRUDDeleteBackendRejectedWhenReferenced(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Seed two backends so that the "at least one backend" check does not mask
	// the "agent still references it" validation.
	seedStoreBackend(t, s, "claude")
	seedStoreBackend(t, s, "codex")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/backends/claude", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE backend still referenced by agent: want non-204, got 204")
	}

	// Backend must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET backend after rejected delete: got %d, want 200", rr.Code)
	}
}

// TestStoreCRUDDeleteSkillRejectedWhenReferenced verifies that deleting a skill
// still referenced by an agent is rejected and leaves the skill intact.
func TestStoreCRUDDeleteSkillRejectedWhenReferenced(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	seedStoreSkill(t, s, "architect")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{"architect"}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/skills/architect", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE skill still referenced by agent: want non-204, got 204")
	}

	// Skill must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/skills/architect", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET skill after rejected delete: got %d, want 200", rr.Code)
	}
}

// ──── Field-level validation tests (webhook layer) ────────────────────────────

func TestStoreCRUDBackendRejectedWithEmptyCommand(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/backends", map[string]any{
		"name": "claude", "command": "", "args": []string{}, "env": map[string]string{},
	})
	if rr.Code == http.StatusOK {
		t.Errorf("POST backend with empty command: want non-200, got 200")
	}
}

func TestStoreCRUDAgentRejectedWithEmptyPrompt(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "",
		"skills": []string{}, "can_dispatch": []string{},
	})
	if rr.Code == http.StatusOK {
		t.Errorf("POST agent with empty prompt: want non-200, got 200")
	}
}

func TestStoreCRUDRepoRejectedWithNoTrigger(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}
	// Binding has no labels, events, or cron — invalid.
	rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder"}},
	})
	if rr.Code == http.StatusOK {
		t.Errorf("POST repo with no-trigger binding: want non-200, got 200")
	}
}

func TestStoreCRUDDeleteBackendRejectedAsLast(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/backends", map[string]any{
		"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create backend: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/backends/claude", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE last backend: want non-204, got 204")
	}

	// Backend must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET backend after rejected delete: got %d, want 200", rr.Code)
	}
}

func TestStoreCRUDDeleteAgentRejectedAsLast(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/agents/coder", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE last agent: want non-204, got 204")
	}
}

// TestStoreCRUDSingleEntityPathCanonicalization verifies that GET and DELETE
// /api/store/{type}/{name} canonicalize the path parameter before lookup so
// that mixed-case names resolve correctly after POST stores them in lowercase.
func TestStoreCRUDSingleEntityPathCanonicalization(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Seed backend + skill with canonical (lowercase) names.
	seedStoreBackend(t, s, "claude")
	seedStoreSkill(t, s, "architect")

	// POST agent with lowercase name — stored as "coder".
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	// GET with mixed-case path — should return 200, not 404.
	rr := doCRUDRequest(t, s, http.MethodGet, "/api/store/agents/Coder", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/store/agents/Coder: got %d, want 200", rr.Code)
	}

	// GET skill with mixed-case path.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/skills/Architect", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/store/skills/Architect: got %d, want 200", rr.Code)
	}

	// GET backend with mixed-case path.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/backends/Claude", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/store/backends/Claude: got %d, want 200", rr.Code)
	}

	// DELETE with mixed-case path — should actually remove the entity.
	rr = doCRUDRequest(t, s, http.MethodDelete, "/api/store/skills/Architect", nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("DELETE /api/store/skills/Architect: got %d, want 204", rr.Code)
	}
	// Confirm it's gone.
	rr = doCRUDRequest(t, s, http.MethodGet, "/api/store/skills/architect", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDDeleteRepoRejectedAsLastEnabled(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/api/store/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create repo: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/api/store/repos/owner/repo", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE last enabled repo: want non-204, got 204")
	}
}
