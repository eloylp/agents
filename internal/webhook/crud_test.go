package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/server"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// openCRUDTestServer creates a test server wired with an in-memory SQLite
// store. Mirrors the wiring cmd/agents performs: NewServer + WithStore +
// fleet handler attached via WithFleet (with SetDB invoked after the
// store is open).
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
	logger := zerolog.Nop()
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, logger)
	s.WithStore(db, nil) // nil reloader — cron hot-reload not exercised here
	fleetHandler := wireFleetForTest(s, cfg, nil, logger)
	fleetHandler.SetDB(db)
	return s
}

// seedStoreBackend inserts a minimal backend into the server's store directly
// so that subsequent agent upserts that reference it pass cross-ref validation.
func seedStoreBackend(t *testing.T, s *Server, name string) {
	t.Helper()
	b := fleet.Backend{Command: name}
	if err := store.UpsertBackend(s.db, name, b); err != nil {
		t.Fatalf("seedStoreBackend %s: %v", name, err)
	}
}

// seedStoreSkill inserts a minimal skill into the server's store directly.
func seedStoreSkill(t *testing.T, s *Server, name string) {
	t.Helper()
	if err := store.UpsertSkill(s.db, name, fleet.Skill{Prompt: "skill prompt"}); err != nil {
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

// ── /agents ────────────────────────────────────────────────────────

func TestStoreCRUDAgentListEmpty(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents: got %d, want 200", rr.Code)
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
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
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
	rr := doCRUDRequest(t, s, http.MethodPost, "/agents", payload)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /agents: got %d, want 200 — %s", rr.Code, rr.Body.String())
	}

	// GET list — should have two entries: pr-reviewer (seeded) + coder.
	rr = doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents: got %d", rr.Code)
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
	rr = doCRUDRequest(t, s, http.MethodGet, "/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/coder: got %d", rr.Code)
	}
}

func TestStoreCRUDAgentDeleteNotFound(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodGet, "/agents/ghost", nil)
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
		if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
			"name": name, "backend": "claude", "prompt": "p",
			"skills": []string{}, "can_dispatch": []string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("seed agent %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/agents/coder", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE: got %d, want 204", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/agents/coder", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentDeleteBlockedByBindings(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	for _, name := range []string{"coder", "reviewer"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
			"name": name, "backend": "claude", "prompt": "p",
			"skills": []string{}, "can_dispatch": []string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("seed agent %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d — %s", rr.Code, rr.Body.String())
	}

	// Plain DELETE must be blocked while the binding references "coder".
	rr := doCRUDRequest(t, s, http.MethodDelete, "/agents/coder", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE without cascade: got %d, want 409", rr.Code)
	}

	// Agent must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET after blocked delete: got %d, want 200", rr.Code)
	}
}

func TestStoreCRUDAgentDeleteCascadeRemovesBindings(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	for _, name := range []string{"coder", "reviewer"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
			"name": name, "backend": "claude", "prompt": "p",
			"skills": []string{}, "can_dispatch": []string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("seed agent %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": true},
			{"agent": "reviewer", "labels": []string{"ai:review"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/agents/coder?cascade=true", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE ?cascade=true: got %d, want 204 — %s", rr.Code, rr.Body.String())
	}

	// Agent is gone.
	rr = doCRUDRequest(t, s, http.MethodGet, "/agents/coder", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after cascade: got %d, want 404", rr.Code)
	}

	// Repo survives with only the reviewer binding.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repo after cascade: got %d, want 200", rr.Code)
	}
	var repo struct {
		Name     string `json:"name"`
		Bindings []struct {
			Agent string `json:"agent"`
		} `json:"bindings"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&repo); err != nil {
		t.Fatalf("decode repo: %v", err)
	}
	if len(repo.Bindings) != 1 || repo.Bindings[0].Agent != "reviewer" {
		t.Errorf("surviving bindings after cascade: got %+v, want [{reviewer}]", repo.Bindings)
	}
}

func TestStoreCRUDAgentMissingName(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{"backend": "claude"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST without name: got %d, want 400", rr.Code)
	}
}

// ── /skills ─────────────────────────────────────────────────────────

func TestStoreCRUDSkillCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/skills", map[string]any{
		"name": "architect", "prompt": "Focus on architecture.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST skill: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/skills/architect", nil)
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

	rr = doCRUDRequest(t, s, http.MethodDelete, "/skills/architect", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE skill: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/skills/architect", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// ── /backends ───────────────────────────────────────────────────────

func TestStoreCRUDBackendCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Create two backends so that deleting one still leaves the system valid.
	for _, name := range []string{"claude", "codex"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
			"name": name, "command": name, "args": []string{}, "env": map[string]string{},
		}); rr.Code != http.StatusOK {
			t.Fatalf("POST backend %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET backend: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/backends/claude", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE backend: got %d", rr.Code)
	}
}

func TestStoreCRUDBackendGetRedactsEnv(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Create a backend that has env entries with secret values.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name":    "claude",
		"command": "claude",
		"args":    []string{},
		"env":     map[string]string{"ANTHROPIC_API_KEY": "sk-secret", "OTHER_VAR": "also-secret"},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST backend: got %d — %s", rr.Code, rr.Body.String())
	}

	// GET list — env values must be redacted.
	rr := doCRUDRequest(t, s, http.MethodGet, "/backends", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET backends: got %d", rr.Code)
	}
	var list []storeBackendJSON
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 backend, got %d", len(list))
	}

	// GET single.
	rr = doCRUDRequest(t, s, http.MethodGet, "/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET backend/claude: got %d", rr.Code)
	}
	var single storeBackendJSON
	if err := json.NewDecoder(rr.Body).Decode(&single); err != nil {
		t.Fatalf("decode single: %v", err)
	}
}


func TestStoreCRUDBackendPatchRuntimeSettings(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name":             "claude",
		"command":          "claude",
		"timeout_seconds":  600,
		"max_prompt_chars": 12000,
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST backend: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{
		"timeout_seconds":  900,
		"max_prompt_chars": 45000,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /backends/claude: got %d — %s", rr.Code, rr.Body.String())
	}

	var out storeBackendJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if out.TimeoutSeconds != 900 {
		t.Fatalf("response timeout_seconds = %d, want 900", out.TimeoutSeconds)
	}
	if out.MaxPromptChars != 45000 {
		t.Fatalf("response max_prompt_chars = %d, want 45000", out.MaxPromptChars)
	}

	backends, err := store.ReadBackends(s.db)
	if err != nil {
		t.Fatalf("ReadBackends: %v", err)
	}
	b, ok := backends["claude"]
	if !ok {
		t.Fatal("backend 'claude' not found after patch")
	}
	if b.TimeoutSeconds != 900 || b.MaxPromptChars != 45000 {
		t.Fatalf("runtime settings = (%d, %d), want (900, 45000)", b.TimeoutSeconds, b.MaxPromptChars)
	}
	if b.Command != "claude" {
		t.Fatalf("command changed unexpectedly: got %q", b.Command)
	}
}

func TestStoreCRUDBackendPatchRuntimeSettingsValidation(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/missing", map[string]any{
		"timeout_seconds": 10,
	}); rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH missing backend: got %d, want 404", rr.Code)
	}

	if rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH empty payload: got %d, want 400", rr.Code)
	}

	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name":    "claude",
		"command": "claude",
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST backend: got %d — %s", rr.Code, rr.Body.String())
	}

	if rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{
		"timeout_seconds": 0,
	}); rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH timeout_seconds=0: got %d, want 400", rr.Code)
	}
	if rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{
		"max_prompt_chars": -1,
	}); rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH max_prompt_chars=-1: got %d, want 400", rr.Code)
	}
}

func TestBackendsLocalCreateNamedAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Local backend creation requires a discovered claude backend.
	if err := store.UpsertBackend(s.db, "claude", fleet.Backend{
		Command: "/bin/sh",
	}); err != nil {
		t.Fatalf("seed claude backend: %v", err)
	}

	rr := doCRUDRequest(t, s, http.MethodPost, "/backends/local", map[string]any{
		"name": "qwen_local",
		"url":  "http://localhost:18000/v1/messages",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /backends/local: got %d — %s", rr.Code, rr.Body.String())
	}
	var created storeBackendJSON
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Name != "qwen_local" {
		t.Fatalf("created backend name: got %q, want %q", created.Name, "qwen_local")
	}
	if created.LocalModelURL != "http://localhost:18000/v1/messages" {
		t.Fatalf("local_model_url: got %q", created.LocalModelURL)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/backends/qwen_local", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /backends/qwen_local: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/backends/qwen_local", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE /backends/qwen_local: got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestBackendsLocalRejectReservedName(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if err := store.UpsertBackend(s.db, "claude", fleet.Backend{
		Command: "/bin/sh",
	}); err != nil {
		t.Fatalf("seed claude backend: %v", err)
	}

	rr := doCRUDRequest(t, s, http.MethodPost, "/backends/local", map[string]any{
		"name": "claude",
		"url":  "http://localhost:18000/v1/messages",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /backends/local with reserved name: got %d — %s", rr.Code, rr.Body.String())
	}
}

// ── /repos ──────────────────────────────────────────────────────────

func TestStoreCRUDRepoCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	// First create the agent that the binding references.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed coder agent: got %d — %s", rr.Code, rr.Body.String())
	}

	// Create two repos so that deleting one still leaves the system valid.
	enabled := true
	for _, name := range []string{"owner/repo", "owner/other"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
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
	rr := doCRUDRequest(t, s, http.MethodGet, "/repos", nil)
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

	// GET single — repo name is owner/repo → /repos/owner/repo
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
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
	rr = doCRUDRequest(t, s, http.MethodDelete, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE repo: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// ── atomic binding CRUD ──────────────────────────────────────────────────────

// seedBindingTestRepo creates a repo + agent pair so the binding test can
// target the /repos/{owner}/{repo}/bindings routes directly.
func seedBindingTestRepo(t *testing.T, s *Server) {
	t.Helper()
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: got %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name":    "owner/repo",
		"enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:seed"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestCreateBindingEndpoint(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/repo/bindings", map[string]any{
		"agent":  "coder",
		"labels": []string{"ai:fix"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create binding: got %d — %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, ok := got["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("expected positive id, got %v", got["id"])
	}
	if got["agent"] != "coder" {
		t.Errorf("agent: got %v", got["agent"])
	}

	// GET the created binding.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo/bindings/"+itoa(int(id)), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET binding: got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestCreateBindingInvalidTriggerReturns400(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/repo/bindings", map[string]any{
		"agent": "coder",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestCreateBindingUnknownRepoReturns404(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/ghost/bindings", map[string]any{
		"agent": "coder", "labels": []string{"x"},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateBindingEndpoint(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/repo/bindings", map[string]any{
		"agent": "coder", "labels": []string{"ai:old"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: got %d — %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&created)
	id := int(created["id"].(float64))

	enabled := false
	rr = doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo/bindings/"+itoa(id), map[string]any{
		"agent":   "coder",
		"cron":    "0 9 * * *",
		"enabled": enabled,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: got %d — %s", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&patched); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if patched["cron"] != "0 9 * * *" {
		t.Errorf("cron not updated: %v", patched["cron"])
	}
	if patched["enabled"] != false {
		t.Errorf("enabled flag not persisted: %v", patched["enabled"])
	}
}

func TestUpdateBindingMismatchedRepoReturns404(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	// Create a second repo with its own binding.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/other", "enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:x"}},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed other: %d %s", rr.Code, rr.Body.String())
	}
	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/other/bindings", map[string]any{
		"agent": "coder", "labels": []string{"ai:one"},
	})
	var created map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&created)
	id := int(created["id"].(float64))

	// PATCH it through the wrong repo's URL.
	rr = doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo/bindings/"+itoa(id), map[string]any{
		"agent": "coder", "labels": []string{"ai:changed"},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteBindingEndpoint(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPost, "/repos/owner/repo/bindings", map[string]any{
		"agent": "coder", "labels": []string{"ai:gone"},
	})
	var created map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&created)
	id := int(created["id"].(float64))

	rr = doCRUDRequest(t, s, http.MethodDelete, "/repos/owner/repo/bindings/"+itoa(id), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d — %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo/bindings/"+itoa(id), nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

func TestPatchRepoTogglesEnabled(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	// Disable via PATCH without sending bindings.
	rr := doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo", map[string]any{
		"enabled": false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH repo: got %d — %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Re-fetch to make sure bindings survived.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repo after patch: got %d", rr.Code)
	}
	var repo map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&repo)
	if repo["enabled"] != false {
		t.Errorf("enabled not persisted: %v", repo["enabled"])
	}
	bindings, _ := repo["bindings"].([]any)
	if len(bindings) == 0 {
		t.Errorf("PATCH must not touch bindings; got %v", repo["bindings"])
	}
}

func TestPatchRepoRejectsEmptyBody(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo", map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty patch, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestGetRepoExposesBindingIDs(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repo: got %d — %s", rr.Code, rr.Body.String())
	}
	var repo map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&repo); err != nil {
		t.Fatalf("decode: %v", err)
	}
	bindings, ok := repo["bindings"].([]any)
	if !ok || len(bindings) == 0 {
		t.Fatalf("expected bindings in response, got %v", repo["bindings"])
	}
	first := bindings[0].(map[string]any)
	id, ok := first["id"].(float64)
	if !ok || id <= 0 {
		t.Errorf("binding missing id: %v", first)
	}
}

// itoa wraps strconv.Itoa locally so the binding tests can construct URLs
// without touching the rest of the file's imports.
func itoa(i int) string { return strconv.Itoa(i) }

// ── reloadCron failure ────────────────────────────────────────────────────────

// errCronReloader satisfies server.CronReloader and always returns an error from Reload.
type errCronReloader struct{ err error }

func (r *errCronReloader) Reload([]fleet.Repo, []fleet.Agent, map[string]fleet.Skill, map[string]fleet.Backend) error {
	return r.err
}

// openCRUDTestServerWithReloader creates a test server wired with a SQLite
// store and the given server.CronReloader.
func openCRUDTestServerWithReloader(t *testing.T, reloader server.CronReloader) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := crudMinimalConfig()
	dc := workflow.NewDataChannels(1)
	logger := zerolog.Nop()
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, logger)
	s.WithStore(db, reloader)
	fleetHandler := wireFleetForTest(s, cfg, nil, logger)
	fleetHandler.SetDB(db)
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
			path:   "/agents",
			body:   map[string]any{"name": "agent-x", "backend": "claude", "prompt": "x"},
			// Seed the backend so cross-ref validation passes and the test
			// genuinely exercises reload failure, not validation failure.
			setup: func(t *testing.T, s *Server) { seedStoreBackend(t, s, "claude") },
		},
		{
			name:   "DELETE agent",
			method: http.MethodDelete,
			path:   "/agents/agent-x",
			body:   nil,
		},
		{
			name:   "POST skill",
			method: http.MethodPost,
			path:   "/skills",
			body:   map[string]any{"name": "arch", "prompt": "Focus on architecture."},
		},
		{
			name:   "DELETE skill",
			method: http.MethodDelete,
			path:   "/skills/arch",
			body:   nil,
		},
		{
			name:   "POST backend",
			method: http.MethodPost,
			path:   "/backends",
			// Use "codex" (a valid backend name) so that validateFleet passes
			// and the only failure is the cron reload.
			body: map[string]any{"name": "codex", "command": "codex", "args": []string{}, "env": map[string]string{}},
			// Seed claude so there is already one backend, making the new backend
			// a valid addition (fleet validation requires at least one).
			setup: func(t *testing.T, s *Server) { seedStoreBackend(t, s, "claude") },
		},
		{
			name:   "DELETE backend",
			method: http.MethodDelete,
			path:   "/backends/claude",
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
			path:   "/repos",
			body:   map[string]any{"name": "owner/repo", "enabled": true},
		},
		{
			name:   "DELETE repo",
			method: http.MethodDelete,
			path:   "/repos/owner/repo",
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

// TestStoreCRUDReloadFailureDoesNotUpdateServerCfg verifies that when
// cronReloader.Reload returns an error, the server's in-memory routing config
// (s.cfg) is NOT updated to the new DB snapshot. Keeping s.cfg on the old
// state ensures the server, scheduler, and engine remain on the same config
// epoch; a split-brain (server serving new repos while the scheduler/engine
// still run on old config) is avoided.
func TestStoreCRUDReloadFailureDoesNotUpdateServerCfg(t *testing.T) {
	t.Parallel()

	reloader := &errCronReloader{err: errors.New("reload broken")}
	s := openCRUDTestServerWithReloader(t, reloader)

	// Pre-condition: server starts with no repos.
	if got := len(s.loadCfg().Repos); got != 0 {
		t.Fatalf("precondition: want 0 repos, got %d", got)
	}

	// Attempt to add a repo via the CRUD API. The write succeeds in the DB but
	// Reload fails, so the handler must return 500 and must NOT swap s.cfg.
	body := map[string]any{"name": "owner/testrepo", "enabled": true}
	rr := doCRUDRequest(t, s, http.MethodPost, "/repos", body)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on reload failure, got %d: %s", rr.Code, rr.Body.String())
	}

	// s.cfg must still reflect the pre-write state (no repos).
	if got := len(s.loadCfg().Repos); got != 0 {
		t.Errorf("server cfg must not be updated on reload failure: want 0 repos, got %d", got)
	}
}

// ── /api/store POST body-size limiting ───────────────────────────────────────

// TestStoreCRUDPostBodySizeLimit verifies that POST write endpoints return
// 413 when the request body exceeds daemon.http.max_body_bytes, including
// the case where the body starts with a valid JSON object followed by
// extra bytes that push the total over the limit.
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
	logger := zerolog.Nop()
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, logger)
	s.WithStore(db, nil)
	fleetHandler := wireFleetForTest(s, cfg, nil, logger)
	fleetHandler.SetDB(db)

	tests := []struct {
		name string
		path string
		body any
	}{
		{
			name: "agent",
			path: "/agents",
			body: map[string]any{"name": "coder", "backend": "claude", "prompt": "You write code — a much longer prompt than 10 bytes."},
		},
		{
			name: "skill",
			path: "/skills",
			body: map[string]any{"name": "arch", "prompt": "You are an architect — longer than 10 bytes."},
		},
		{
			name: "backend",
			path: "/backends",
			body: map[string]any{"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{}},
		},
		{
			name: "repo",
			path: "/repos",
			body: map[string]any{"name": "owner/repo", "enabled": true, "bindings": []any{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := doCRUDRequest(t, s, http.MethodPost, tc.path, tc.body)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("POST %s: want 413 for oversized body, got %d: %s", tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestStoreCRUDPostBodyTrailingGarbageRejected verifies that POST endpoints
// reject a body that contains a valid JSON object followed by extra bytes that
// push the total past max_body_bytes. The old io.LimitReader approach allowed
// these through because the JSON decoder stopped reading after the first value.
func TestStoreCRUDPostBodyTrailingGarbageRejected(t *testing.T) {
	t.Parallel()

	cfg := crudMinimalConfig()
	cfg.Daemon.HTTP.MaxBodyBytes = 15 // {"name":"x"} is 12 bytes; +5 garbage exceeds limit

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dc := workflow.NewDataChannels(1)
	logger := zerolog.Nop()
	s := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, logger)
	s.WithStore(db, nil)
	fleetHandler := wireFleetForTest(s, cfg, nil, logger)
	fleetHandler.SetDB(db)

	endpoints := []string{
		"/agents",
		"/skills",
		"/backends",
		"/repos",
	}

	for _, path := range endpoints {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			// Craft a body: valid minimal JSON (12 bytes) + 5 bytes of garbage
			// that push the total to 17 bytes, over the 15-byte limit.
			rawBody := []byte(`{"name":"x"}XXXXX`)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(rawBody))
			rr := httptest.NewRecorder()
			s.buildHandler().ServeHTTP(rr, req)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("POST %s with valid JSON + trailing garbage: want 413, got %d: %s",
					path, rr.Code, rr.Body.String())
			}
		})
	}
}

// ── /api/store mutation error classification ──────────────────────────────────

// TestStoreCRUDValidationErrorReturns400 verifies that POST endpoints return
// 400 when the store rejects the input due to invalid field values.
func TestStoreCRUDValidationErrorReturns400(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		path  string
		body  any
		setup func(t *testing.T, s *Server)
	}{
		{
			name: "backend with empty command",
			path: "/backends",
			body: map[string]any{"name": "claude", "command": "   ", "args": []string{}, "env": map[string]string{}},
		},
		{
			name: "agent with empty prompt",
			path: "/agents",
			body: map[string]any{"name": "coder", "backend": "claude", "prompt": "",
				"skills": []string{}, "can_dispatch": []string{}},
			setup: func(t *testing.T, s *Server) { seedStoreBackend(t, s, "claude") },
		},
		{
			name: "agent with unknown backend",
			path: "/agents",
			body: map[string]any{"name": "coder", "backend": "unknown", "prompt": "p",
				"skills": []string{}, "can_dispatch": []string{}},
		},
		{
			name: "repo with no-trigger binding",
			path: "/repos",
			body: map[string]any{
				"name": "owner/repo", "enabled": true,
				"bindings": []map[string]any{{"agent": "coder"}},
			},
			setup: func(t *testing.T, s *Server) {
				seedStoreBackend(t, s, "claude")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openCRUDTestServer(t)
			if tc.setup != nil {
				tc.setup(t, s)
			}
			rr := doCRUDRequest(t, s, http.MethodPost, tc.path, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: want 400 for invalid input, got %d: %s", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestStoreCRUDConflictErrorReturns409 verifies that DELETE endpoints return
// 409 when the deletion would violate a cardinality or reference constraint.
func TestStoreCRUDConflictErrorReturns409(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		path  string
		setup func(t *testing.T, s *Server)
	}{
		{
			name: "delete last backend",
			path: "/backends/claude",
			setup: func(t *testing.T, s *Server) {
				if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
					"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create backend: got %d — %s", rr.Code, rr.Body.String())
				}
			},
		},
		{
			name: "delete last agent",
			path: "/agents/coder",
			setup: func(t *testing.T, s *Server) {
				seedStoreBackend(t, s, "claude")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
				}
			},
		},
		{
			name: "delete backend referenced by agent",
			path: "/backends/claude",
			setup: func(t *testing.T, s *Server) {
				seedStoreBackend(t, s, "claude")
				seedStoreBackend(t, s, "codex")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openCRUDTestServer(t)
			tc.setup(t, s)
			rr := doCRUDRequest(t, s, http.MethodDelete, tc.path, nil)
			if rr.Code != http.StatusConflict {
				t.Errorf("%s: want 409 for constraint violation, got %d: %s", tc.name, rr.Code, rr.Body.String())
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
	last  []fleet.Agent
}

func (r *countingCronReloader) Reload(_ []fleet.Repo, agents []fleet.Agent, _ map[string]fleet.Skill, _ map[string]fleet.Backend) error {
	atomic.AddInt32(&r.calls, 1)
	r.mu.Lock()
	r.last = agents
	r.mu.Unlock()
	return nil
}

// TestConcurrentWriteReloadSerialisation verifies that concurrent POST
// /agents requests do not interleave their DB-write and in-memory
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
			rr := doCRUDRequest(t, s, http.MethodPost, "/agents",
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

// TestStoreCRUDDeleteBackendRejectedWhenReferenced verifies that deleting a
// backend still referenced by an agent is rejected and leaves the backend intact.
func TestStoreCRUDDeleteBackendRejectedWhenReferenced(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Seed two backends so that the "at least one backend" check does not mask
	// the "agent still references it" validation.
	seedStoreBackend(t, s, "claude")
	seedStoreBackend(t, s, "codex")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/backends/claude", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE backend still referenced by agent: want non-204, got 204")
	}

	// Backend must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/backends/claude", nil)
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
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{"architect"}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/skills/architect", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE skill still referenced by agent: want non-204, got 204")
	}

	// Skill must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/skills/architect", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET skill after rejected delete: got %d, want 200", rr.Code)
	}
}

func TestStoreCRUDDeleteBackendRejectedAsLast(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create backend: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/backends/claude", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE last backend: want non-204, got 204")
	}

	// Backend must still be present.
	rr = doCRUDRequest(t, s, http.MethodGet, "/backends/claude", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET backend after rejected delete: got %d, want 200", rr.Code)
	}
}

func TestStoreCRUDDeleteAgentRejectedAsLast(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/agents/coder", nil)
	if rr.Code == http.StatusNoContent {
		t.Error("DELETE last agent: want non-204, got 204")
	}
}

// TestStoreCRUDSingleEntityPathCanonicalization verifies that GET and DELETE
// /{type}/{name} canonicalize the path parameter before lookup so
// that mixed-case names resolve correctly after POST stores them in lowercase.
func TestStoreCRUDSingleEntityPathCanonicalization(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Seed backend + skill with canonical (lowercase) names.
	seedStoreBackend(t, s, "claude")
	seedStoreSkill(t, s, "architect")

	// POST agent with lowercase name — stored as "coder".
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}

	// GET with mixed-case path — should return 200, not 404.
	rr := doCRUDRequest(t, s, http.MethodGet, "/agents/Coder", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /agents/Coder: got %d, want 200", rr.Code)
	}

	// GET skill with mixed-case path.
	rr = doCRUDRequest(t, s, http.MethodGet, "/skills/Architect", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /skills/Architect: got %d, want 200", rr.Code)
	}

	// GET backend with mixed-case path.
	rr = doCRUDRequest(t, s, http.MethodGet, "/backends/Claude", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /backends/Claude: got %d, want 200", rr.Code)
	}

	// DELETE with mixed-case path — should actually remove the entity.
	rr = doCRUDRequest(t, s, http.MethodDelete, "/skills/Architect", nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("DELETE /skills/Architect: got %d, want 204", rr.Code)
	}
	// Confirm it's gone.
	rr = doCRUDRequest(t, s, http.MethodGet, "/skills/architect", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// TestStoreCRUDRepoPathCanonicalization verifies that GET and DELETE
// /repos/{owner}/{repo} canonicalize the path parameter
// case-insensitively, matching the config layer's RepoByName semantics.
func TestStoreCRUDRepoPathCanonicalization(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: got %d — %s", rr.Code, rr.Body.String())
	}

	// Create two repos so that deleting one still leaves the fleet valid.
	for _, name := range []string{"owner/repo", "owner/other"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
			"name": name, "enabled": true,
			"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai"}, "enabled": true}},
		}); rr.Code != http.StatusOK {
			t.Fatalf("POST repo %s: got %d — %s", name, rr.Code, rr.Body.String())
		}
	}

	// GET with mixed-case owner — should return 200, not 404.
	rr := doCRUDRequest(t, s, http.MethodGet, "/repos/Owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /repos/Owner/repo: got %d, want 200", rr.Code)
	}

	// GET with mixed-case repo segment — should return 200.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/Repo", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /repos/owner/Repo: got %d, want 200", rr.Code)
	}

	// DELETE with mixed-case path — should actually remove the repo.
	rr = doCRUDRequest(t, s, http.MethodDelete, "/repos/Owner/Repo", nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("DELETE /repos/Owner/Repo: got %d, want 204", rr.Code)
	}
	// Confirm it's gone.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// TestStoreCRUDPostReturnsCanonicalForm verifies that POST endpoints return the
// canonical persisted entity rather than the raw request body. Values that
// storage normalises (lowercase names, trimmed whitespace, applied backend
// defaults) must be reflected in the POST response so that clients doing
// optimistic updates from the response never cache a shape that disagrees with
// the very next GET.
func TestStoreCRUDPostReturnsCanonicalForm(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// ── backend ──────────────────────────────────────────────────────────────
	// POST with mixed-case name and zero timeout/max_prompt_chars; response must
	// have lowercase name, defaults applied, and env values redacted.
	rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name":    "Claude",
		"command": " claude ",
		"env":     map[string]string{"ANTHROPIC_API_KEY": "secret-value"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST backend: got %d — %s", rr.Code, rr.Body.String())
	}
	var backend map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&backend); err != nil {
		t.Fatalf("decode backend response: %v", err)
	}
	if backend["name"] != "claude" {
		t.Errorf("backend name: got %q, want %q", backend["name"], "claude")
	}
	if backend["command"] != "claude" {
		t.Errorf("backend command not trimmed: got %q, want %q", backend["command"], "claude")
	}
	// Defaults must be applied (0 → non-zero).
	if to, ok := backend["timeout_seconds"].(float64); !ok || to == 0 {
		t.Errorf("backend timeout_seconds: got %v, want non-zero default", backend["timeout_seconds"])
	}
	if mp, ok := backend["max_prompt_chars"].(float64); !ok || mp == 0 {
		t.Errorf("backend max_prompt_chars: got %v, want non-zero default", backend["max_prompt_chars"])
	}
	// ── agent ────────────────────────────────────────────────────────────────
	// POST with mixed-case name and extra whitespace; response must have
	// lowercase name and trimmed prompt.
	rr = doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name":    "  Coder  ",
		"backend": "claude",
		"prompt":  "  You write code.  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST agent: got %d — %s", rr.Code, rr.Body.String())
	}
	var agent map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&agent); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if agent["name"] != "coder" {
		t.Errorf("agent name: got %q, want %q", agent["name"], "coder")
	}
	if agent["prompt"] != "You write code." {
		t.Errorf("agent prompt not trimmed: got %q", agent["prompt"])
	}

	// ── skill ────────────────────────────────────────────────────────────────
	// POST with mixed-case name and whitespace-padded prompt; response must
	// have lowercase name and trimmed prompt.
	rr = doCRUDRequest(t, s, http.MethodPost, "/skills", map[string]any{
		"name":   "Architect",
		"prompt": "  Focus on design.  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST skill: got %d — %s", rr.Code, rr.Body.String())
	}
	var skill map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&skill); err != nil {
		t.Fatalf("decode skill response: %v", err)
	}
	if skill["name"] != "architect" {
		t.Errorf("skill name: got %q, want %q", skill["name"], "architect")
	}
	if skill["prompt"] != "Focus on design." {
		t.Errorf("skill prompt not trimmed: got %q", skill["prompt"])
	}

	// ── repo ─────────────────────────────────────────────────────────────────
	// POST with mixed-case name; response must have lowercase name and
	// normalized binding agent name.
	rr = doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name":    "Owner/Repo",
		"enabled": true,
		"bindings": []map[string]any{
			{"agent": "  Coder  ", "labels": []string{"ai:fix"}},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST repo: got %d — %s", rr.Code, rr.Body.String())
	}
	var repo map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&repo); err != nil {
		t.Fatalf("decode repo response: %v", err)
	}
	if repo["name"] != "owner/repo" {
		t.Errorf("repo name: got %q, want %q", repo["name"], "owner/repo")
	}
	bindings, _ := repo["bindings"].([]any)
	if len(bindings) == 0 {
		t.Fatal("repo bindings missing in response")
	}
	if b0, ok := bindings[0].(map[string]any); ok {
		if b0["agent"] != "coder" {
			t.Errorf("binding agent name: got %q, want %q", b0["agent"], "coder")
		}
	} else {
		t.Errorf("binding[0] wrong type: %T", bindings[0])
	}
}

// TestServerCfgUpdatedAfterCRUDWrite verifies that the webhook server's
// in-memory routing config is updated immediately after a CRUD write so that
// a newly-added repo is accepted by the webhook event path and visible in
// /api/agents — without requiring a restart.
//
// This is a regression test for the finding that Server kept using its startup
// s.cfg snapshot for /webhooks/github and /api/agents after CRUD writes, while
// only the scheduler/engine were updated via cronReloader.Reload.
func TestServerCfgUpdatedAfterCRUDWrite(t *testing.T) {
	t.Parallel()

	s := openCRUDTestServer(t)
	// Confirm the initial config has no repos and no agents.
	if len(s.loadCfg().Repos) != 0 {
		t.Fatalf("precondition: expected 0 repos, got %d", len(s.loadCfg().Repos))
	}

	// Seed backend and create agent + repo via CRUD API.
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/newrepo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST repo: %d — %s", rr.Code, rr.Body.String())
	}

	// Verify the in-memory config was updated: the new repo must be present.
	cfg := s.loadCfg()
	if len(cfg.Repos) != 1 || cfg.Repos[0].Name != "owner/newrepo" {
		t.Fatalf("server cfg not updated: repos = %v", cfg.Repos)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "coder" {
		t.Fatalf("server cfg not updated: agents = %v", cfg.Agents)
	}

	// Verify /api/agents reflects the new agent.
	rr := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/agents: %d — %s", rr.Code, rr.Body.String())
	}
	var apiAgents []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&apiAgents); err != nil {
		t.Fatalf("decode /api/agents: %v", err)
	}
	if len(apiAgents) != 1 {
		t.Fatalf("/api/agents: want 1 agent, got %d", len(apiAgents))
	}
	if apiAgents[0]["name"] != "coder" {
		t.Errorf("/api/agents[0].name: got %v, want coder", apiAgents[0]["name"])
	}

	// Verify the webhook event path accepts the new repo. Send an issues.labeled
	// event for owner/newrepo; it should be enqueued (202) rather than silently
	// dropped because the repo was absent from the startup config.
	body := `{"action":"labeled","label":{"name":"ai:fix"},"issue":{"number":1},"repository":{"full_name":"owner/newrepo"},"sender":{"login":"user"}}`
	sig := signatureForTests([]byte(body), "")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "delivery-id-1")
	req.Header.Set("X-Hub-Signature-256", sig)
	// Webhook secret is empty (crudMinimalConfig default) — verifySignature
	// requires non-empty secret, so the request will be rejected as unauthorized
	// if routed; but the repo gate runs before enqueue. We test the repo gate by
	// observing whether the handler returns 401 (signature check, meaning it got
	// past the "repo not found" early-return) vs 202 (repo not found silently
	// ignored). With the repo absent, the handler returns 202 immediately (no
	// event enqueued). With the repo present, it proceeds to signature check.
	// Since the server has no webhook secret configured, verifySignature returns
	// false and the handler returns 401 — which proves the routing reached the
	// signature check gate, i.e., the repo was found in the updated config.
	rr2 := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr2, req)
	// 401 means signature check ran, which only happens after the repo gate
	// passes: the new repo was found in the post-write in-memory config.
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("webhook after CRUD repo add: want 401 (signature check = repo found), got %d — body: %s",
			rr2.Code, rr2.Body.String())
	}
}

// TestStoreCRUDDeleteRepoAllowsLastEnabled verifies that the HTTP DELETE
// /repos/{owner}/{repo} endpoint succeeds with 204 even when the target is the
// last (or only) enabled repo. Disabling/removing all repos is a legitimate
// user action; the daemon runs cleanly with zero enabled repos. Regression for
// issue #302.
func TestStoreCRUDDeleteRepoAllowsLastEnabled(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create repo: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE last enabled repo: want 204, got %d — %s", rr.Code, rr.Body.String())
	}

	// The repo must be gone from subsequent reads.
	if rr := doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil); rr.Code != http.StatusNotFound {
		t.Errorf("GET deleted repo: want 404, got %d — %s", rr.Code, rr.Body.String())
	}
}

// ── /export and /import ───────────────────────────────────

func TestStoreExportReturnsYAML(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	// Create an agent and repo so the export is non-trivial.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "help",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create repo: %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/export", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: got %d — %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "coder") {
		t.Errorf("export YAML missing agent name: %s", body)
	}
	if !strings.Contains(body, "owner/repo") {
		t.Errorf("export YAML missing repo name: %s", body)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "yaml") {
		t.Errorf("export Content-Type want yaml, got %q", ct)
	}
}

func TestStoreImportRoundTrip(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	// Seed a repo so the initial state isn't empty (import validation requires
	// at least one enabled repo). We import a repo in the YAML payload so this
	// is satisfied by the import itself. Seed one first so the import can be
	// verified as an upsert.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "scout", "backend": "claude", "prompt": "x",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: %d", rr.Code)
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/seed-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "scout", "labels": []string{"ai:scan"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d — %s", rr.Code, rr.Body.String())
	}

	yaml := `agents:
  - name: imported-agent
    backend: claude
    prompt: imported prompt
    skills: []
    can_dispatch: []
skills:
  imported-skill:
    prompt: imported skill prompt
`
	req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(yaml))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: got %d — %s", rr.Code, rr.Body.String())
	}

	var summary map[string]int
	if err := json.NewDecoder(rr.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary["agents"] != 1 {
		t.Errorf("import agents count: want 1, got %d", summary["agents"])
	}
	if summary["skills"] != 1 {
		t.Errorf("import skills count: want 1, got %d", summary["skills"])
	}

	// Verify the imported agent appears in the list.
	rr2 := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if !strings.Contains(rr2.Body.String(), "imported-agent") {
		t.Errorf("imported agent not in agent list: %s", rr2.Body.String())
	}
}

func TestStoreImportReplacePrunesExistingRecords(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	// Seed an agent and a repo that should be absent after the replace import.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "old-agent", "backend": "claude", "prompt": "old",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed old-agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/old-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "old-agent", "labels": []string{"ai:scan"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed old-repo: %d — %s", rr.Code, rr.Body.String())
	}

	// Replace with a YAML that contains only a new agent + repo (no old-agent).
	yamlBody := `
daemon:
  ai_backends:
    claude:
      command: claude
      args: []
agents:
  - name: new-agent
    backend: claude
    prompt: fresh
    skills: []
    can_dispatch: []
repos:
  - name: owner/new-repo
    enabled: true
    bindings:
      - agent: new-agent
        labels:
          - ai:run
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("replace import: got %d — %s", rr.Code, rr.Body.String())
	}

	// old-agent must be gone.
	agents := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if strings.Contains(agents.Body.String(), "old-agent") {
		t.Errorf("old-agent still present after replace import: %s", agents.Body.String())
	}
	if !strings.Contains(agents.Body.String(), "new-agent") {
		t.Errorf("new-agent missing after replace import: %s", agents.Body.String())
	}

	// old-repo must be gone.
	repos := doCRUDRequest(t, s, http.MethodGet, "/repos", nil)
	if strings.Contains(repos.Body.String(), "old-repo") {
		t.Errorf("old-repo still present after replace import: %s", repos.Body.String())
	}
	if !strings.Contains(repos.Body.String(), "new-repo") {
		t.Errorf("new-repo missing after replace import: %s", repos.Body.String())
	}
}

func TestStoreImportRejectsEmptyFleetOnBlankStore(t *testing.T) {
	t.Parallel()
	// Blank store: no agents, no repos, no backends.
	s := openCRUDTestServer(t)

	// Import with only skills — should fail because the resulting store would
	// have no agents, no enabled repos, and no backends.
	yamlBody := `skills:
  my-skill:
    prompt: just a skill
`
	req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("import skills-only on blank store: want 400, got %d — %s", rr.Code, rr.Body.String())
	}
}

func TestStoreReplaceRejectsEmptyAgentList(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	// Seed an agent and repo so the store is in a valid state.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "existing-agent", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/r", "enabled": true,
		"bindings": []map[string]any{{"agent": "existing-agent", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d — %s", rr.Code, rr.Body.String())
	}

	// Replace with a YAML that contains a backend but no agents — should fail.
	yamlBody := `daemon:
  ai_backends:
    claude:
      command: claude
      args: []
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("replace with no agents: want 400, got %d — %s", rr.Code, rr.Body.String())
	}

	// The original agent must still be present — the failed replace must not
	// have modified the store.
	agents := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if !strings.Contains(agents.Body.String(), "existing-agent") {
		t.Errorf("existing-agent missing after failed replace: %s", agents.Body.String())
	}
}

func TestStoreImportRejectsInvalidMode(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/import?mode=replce", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid mode: want 400, got %d — %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid mode") {
		t.Errorf("error body should mention invalid mode, got: %s", rr.Body.String())
	}
}

func TestStoreImportMergeRejectsInvalidCron(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "scout", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/existing-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "scout", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d — %s", rr.Code, rr.Body.String())
	}

	yamlBody := `
daemon:
  ai_backends:
    claude:
      command: claude
      args: []
agents:
  - name: scout
    backend: claude
    prompt: p
    skills: []
    can_dispatch: []
repos:
  - name: owner/existing-repo
    enabled: true
    use:
      - agent: scout
        cron: "99 99 * * *"
`
	req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("merge import with invalid cron: want 400, got %d — %s", rr.Code, rr.Body.String())
	}

	// The original repo must still be intact after the failed import.
	repos := doCRUDRequest(t, s, http.MethodGet, "/repos", nil)
	if !strings.Contains(repos.Body.String(), "existing-repo") {
		t.Errorf("existing-repo missing after failed merge import: %s", repos.Body.String())
	}
}

func TestStoreImportReplaceRejectsInvalidCron(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "scout", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: %d — %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/existing-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "scout", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d — %s", rr.Code, rr.Body.String())
	}

	yamlBody := `
daemon:
  ai_backends:
    claude:
      command: claude
      args: []
agents:
  - name: scout
    backend: claude
    prompt: p
    skills: []
    can_dispatch: []
repos:
  - name: owner/new-repo
    enabled: true
    use:
      - agent: scout
        cron: "1-2-3 * * * *"
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.buildHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("replace import with invalid cron: want 400, got %d — %s", rr.Code, rr.Body.String())
	}

	// The original state must be preserved after the failed replace.
	agents := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if !strings.Contains(agents.Body.String(), "scout") {
		t.Errorf("scout missing after failed replace import: %s", agents.Body.String())
	}
	repos := doCRUDRequest(t, s, http.MethodGet, "/repos", nil)
	if !strings.Contains(repos.Body.String(), "existing-repo") {
		t.Errorf("existing-repo missing after failed replace import: %s", repos.Body.String())
	}
}

// ── PATCH /agents/{name} ────────────────────────────────────────────

func TestStoreCRUDAgentPatchSingleField(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	seedStoreBackend(t, s, "codex")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "model": "opus",
		"prompt": "p", "description": "d",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed coder: got %d — %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"backend": "codex",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /agents/coder: got %d — %s", rr.Code, rr.Body.String())
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Backend != "codex" {
		t.Fatalf("backend: got %q, want %q", out.Backend, "codex")
	}
	if out.Model != "opus" || out.Prompt != "p" || out.Description != "d" {
		t.Fatalf("non-patched fields drifted: %+v", out)
	}
}

func TestStoreCRUDAgentPatchEmptyBodyRejected(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: %s", rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty PATCH: got %d, want 400", rr.Code)
	}
}

func TestStoreCRUDAgentPatchNotFound(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	if rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/missing", map[string]any{
		"prompt": "new",
	}); rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH missing: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentPatchValidationFailsFast(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: %s", rr.Body.String())
	}
	// Unknown backend must surface as a store validation error (400 via storeErrStatus).
	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"backend": "nope",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown backend: got %d, want 400 — %s", rr.Code, rr.Body.String())
	}
}

// ── PATCH /skills/{name} ────────────────────────────────────────────

func TestStoreCRUDSkillPatchPrompt(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreSkill(t, s, "architect")
	rr := doCRUDRequest(t, s, http.MethodPatch, "/skills/architect", map[string]any{
		"prompt": "new architecture guidance",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /skills/architect: got %d — %s", rr.Code, rr.Body.String())
	}
	var out storeSkillJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Prompt != "new architecture guidance" {
		t.Fatalf("prompt: got %q", out.Prompt)
	}
}

func TestStoreCRUDSkillPatchEmptyBodyRejected(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreSkill(t, s, "architect")
	if rr := doCRUDRequest(t, s, http.MethodPatch, "/skills/architect", map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty PATCH skill: got %d, want 400", rr.Code)
	}
}

func TestStoreCRUDSkillPatchNotFound(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	if rr := doCRUDRequest(t, s, http.MethodPatch, "/skills/missing", map[string]any{
		"prompt": "x",
	}); rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH missing skill: got %d, want 404", rr.Code)
	}
}

// ── PATCH /backends/{name} — superset shape ─────────────────────────

func TestStoreCRUDBackendPatchFullShape(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name": "claude", "command": "/usr/bin/claude",
		"timeout_seconds": 600, "max_prompt_chars": 12000,
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: %s", rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{
		"command": "/opt/claude/bin/claude",
		"models":  []string{"opus", "sonnet"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH backend: got %d — %s", rr.Code, rr.Body.String())
	}
	var out storeBackendJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Command != "/opt/claude/bin/claude" {
		t.Fatalf("command: got %q", out.Command)
	}
	if len(out.Models) != 2 {
		t.Fatalf("models: got %v", out.Models)
	}
	if out.TimeoutSeconds != 600 {
		t.Fatalf("timeout_seconds drifted: got %d, want 600", out.TimeoutSeconds)
	}
}
