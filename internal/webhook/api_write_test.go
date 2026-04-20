package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// openWriteTestServer creates a Server wired to a fresh in-memory SQLite DB
// pre-loaded with minimalStore config. It returns the server and a cleanup func.
func openWriteTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Seed the DB with the minimal fleet so daemon config is present.
	seed := minimalStoreCfg()
	if err := store.Import(db, seed); err != nil {
		db.Close()
		t.Fatalf("import seed: %v", err)
	}

	// Use store.Load (not LoadAndValidate) to avoid env-var resolution in tests.
	// Set secrets directly so the server has a valid webhook secret and API key.
	cfg, err := store.Load(db)
	if err != nil {
		db.Close()
		t.Fatalf("load: %v", err)
	}
	cfg.Daemon.HTTP.WebhookSecret = "test-webhook-secret"
	cfg.Daemon.HTTP.APIKey = testAPIKey

	dc := workflow.NewDataChannels(1)
	srv := NewServer(cfg, NewDeliveryStore(0), dc, nil, nil, zerolog.Nop())
	srv.WithStore(db)
	return srv, func() { db.Close() }
}

// minimalStoreCfg returns a *config.Config that is valid enough for
// store.Import — it has daemon settings, one backend, one agent, and one repo
// with no bindings. No validation is run (LoadAndValidate handles that).
func minimalStoreCfg() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			Log: config.LogConfig{Level: "info", Format: "text"},
			HTTP: config.HTTPConfig{
				ListenAddr:             ":0",
				StatusPath:             "/status",
				WebhookPath:            "/webhooks/github",
				AgentsRunPath:          "/agents/run",
				WebhookSecretEnv:       "GITHUB_WEBHOOK_SECRET",
				WebhookSecret:          "secret",
				APIKey:                 testAPIKey,
				ReadTimeoutSeconds:     15,
				WriteTimeoutSeconds:    15,
				IdleTimeoutSeconds:     60,
				MaxBodyBytes:           1 << 20,
				DeliveryTTLSeconds:     3600,
				ShutdownTimeoutSeconds: 15,
			},
			Processor: config.ProcessorConfig{
				EventQueueBuffer:    256,
				MaxConcurrentAgents: 4,
				Dispatch: config.DispatchConfig{
					MaxDepth:           3,
					MaxFanout:          4,
					DedupWindowSeconds: 300,
				},
			},
			MemoryDir: "/var/lib/agents/memory",
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {
					Command:        "claude",
					Args:           []string{"-p"},
					Env:            map[string]string{},
					TimeoutSeconds: 600,
					MaxPromptChars: 12000,
				},
			},
		},
		Agents: []config.AgentDef{
			{Name: "coder", Backend: "claude", Prompt: "Write code."},
		},
		Repos: []config.RepoDef{
			{Name: "owner/repo", Enabled: true},
		},
	}
}

// authHeader returns the Bearer auth header for the test API key.
func authHeader() string { return "Bearer " + testAPIKey }

// postJSON builds a request with a JSON body.
func postJSON(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ── /api/agents ─────────────────────────────────────────────────────────────

func TestHandlePutAgent(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	body := putAgentRequest{
		Name:    "reviewer",
		Backend: "claude",
		Prompt:  "Review PRs carefully.",
	}
	req := postJSON(t, http.MethodPut, "/api/agents", body)
	rec := httptest.NewRecorder()
	srv.handlePutAgent(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify the config was refreshed.
	found := false
	for _, a := range srv.cfg().Agents {
		if a.Name == "reviewer" && a.Prompt == "Review PRs carefully." {
			found = true
		}
	}
	if !found {
		t.Error("put agent not visible in live config")
	}
}

func TestHandlePutAgentMissingName(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := postJSON(t, http.MethodPut, "/api/agents", putAgentRequest{Backend: "claude"})
	rec := httptest.NewRecorder()
	srv.handlePutAgent(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestHandleDeleteAgent(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	// First add an agent with no bindings so deletion is safe.
	addReq := postJSON(t, http.MethodPut, "/api/agents",
		putAgentRequest{Name: "temp", Backend: "claude", Prompt: "Temp."})
	srv.handlePutAgent(httptest.NewRecorder(), addReq)

	req := httptest.NewRequest(http.MethodDelete, "/api/agents/temp", nil)
	req.Header.Set("Authorization", authHeader())
	rec := httptest.NewRecorder()

	// Inject mux vars.
	req = muxVars(req, map[string]string{"name": "temp"})
	srv.handleDeleteAgent(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, a := range srv.cfg().Agents {
		if a.Name == "temp" {
			t.Error("deleted agent still visible in live config")
		}
	}
}

// ── /api/skills ─────────────────────────────────────────────────────────────

func TestHandlePutSkill(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := postJSON(t, http.MethodPut, "/api/skills",
		putSkillRequest{Name: "security", Prompt: "Think about security."})
	rec := httptest.NewRecorder()
	srv.handlePutSkill(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if srv.cfg().Skills["security"].Prompt != "Think about security." {
		t.Error("skill not visible in live config after put")
	}
}

func TestHandleDeleteSkill(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	// Seed a skill to delete.
	seedReq := postJSON(t, http.MethodPut, "/api/skills",
		putSkillRequest{Name: "temp-skill", Prompt: "Temp."})
	srv.handlePutSkill(httptest.NewRecorder(), seedReq)

	req := httptest.NewRequest(http.MethodDelete, "/api/skills/temp-skill", nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{"name": "temp-skill"})
	rec := httptest.NewRecorder()
	srv.handleDeleteSkill(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if _, ok := srv.cfg().Skills["temp-skill"]; ok {
		t.Error("deleted skill still visible in live config")
	}
}

// ── /api/backends ────────────────────────────────────────────────────────────

func TestHandlePutBackend(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := postJSON(t, http.MethodPut, "/api/backends", putBackendRequest{
		Name:           "codex",
		Command:        "codex",
		Args:           []string{"--model", "gpt-4"},
		Env:            map[string]string{},
		TimeoutSeconds: 300,
		MaxPromptChars: 8000,
	})
	rec := httptest.NewRecorder()
	srv.handlePutBackend(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := srv.cfg().Daemon.AIBackends["codex"]; !ok {
		t.Error("backend not visible in live config after put")
	}
}

func TestHandleDeleteBackend(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	addReq := postJSON(t, http.MethodPut, "/api/backends", putBackendRequest{
		Name: "extra", Command: "extra", Env: map[string]string{}, TimeoutSeconds: 60,
	})
	srv.handlePutBackend(httptest.NewRecorder(), addReq)

	req := httptest.NewRequest(http.MethodDelete, "/api/backends/extra", nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{"name": "extra"})
	rec := httptest.NewRecorder()
	srv.handleDeleteBackend(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if _, ok := srv.cfg().Daemon.AIBackends["extra"]; ok {
		t.Error("deleted backend still present in live config")
	}
}

// ── /api/repos ───────────────────────────────────────────────────────────────

func TestHandlePutRepo(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := postJSON(t, http.MethodPut, "/api/repos",
		putRepoRequest{Name: "owner/new-repo", Enabled: true})
	rec := httptest.NewRecorder()
	srv.handlePutRepo(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/new-repo" && r.Enabled {
			found = true
		}
	}
	if !found {
		t.Error("new repo not visible in live config")
	}
}

func TestHandleDeleteRepo(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	// Add then delete.
	addReq := postJSON(t, http.MethodPut, "/api/repos",
		putRepoRequest{Name: "owner/tmp", Enabled: true})
	srv.handlePutRepo(httptest.NewRecorder(), addReq)

	req := httptest.NewRequest(http.MethodDelete, "/api/repos/owner/tmp", nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{"name": "owner/tmp"})
	rec := httptest.NewRecorder()
	srv.handleDeleteRepo(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/tmp" {
			t.Error("deleted repo still visible in live config")
		}
	}
}

// ── /api/repos/{name}/bindings ───────────────────────────────────────────────

func TestHandlePutBinding(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := postJSON(t, http.MethodPost, "/api/repos/owner~repo/bindings",
		putBindingRequest{Agent: "coder", Events: []string{"push"}})
	req = muxVars(req, map[string]string{"name": "owner/repo"})
	rec := httptest.NewRecorder()
	srv.handlePutBinding(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp putBindingResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == 0 {
		t.Error("want non-zero binding ID in response")
	}
	// Verify it is visible in the reloaded config.
	found := false
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/repo" {
			for _, b := range r.Use {
				if b.Agent == "coder" && len(b.Events) == 1 && b.Events[0] == "push" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("new binding not visible in live config")
	}
}

func TestHandleDeleteBinding(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	// Add a binding to get its ID.
	addReq := postJSON(t, http.MethodPost, "/api/repos/owner~repo/bindings",
		putBindingRequest{Agent: "coder", Cron: "0 * * * *"})
	addReq = muxVars(addReq, map[string]string{"name": "owner/repo"})
	addRec := httptest.NewRecorder()
	srv.handlePutBinding(addRec, addReq)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add binding: want 201, got %d", addRec.Code)
	}
	var addResp putBindingResponse
	if err := json.NewDecoder(addRec.Body).Decode(&addResp); err != nil {
		t.Fatalf("decode add response: %v", err)
	}

	beforeCount := 0
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/repo" {
			beforeCount = len(r.Use)
		}
	}

	// Now delete it.
	delPath := "/api/repos/owner~repo/bindings/" + intStr(addResp.ID)
	req := httptest.NewRequest(http.MethodDelete, delPath, nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{
		"name": "owner/repo",
		"id":   intStr(addResp.ID),
	})
	rec := httptest.NewRecorder()
	srv.handleDeleteBinding(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	afterCount := 0
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/repo" {
			afterCount = len(r.Use)
		}
	}
	if afterCount != beforeCount-1 {
		t.Errorf("binding count: want %d, got %d", beforeCount-1, afterCount)
	}
}

func TestHandleDeleteBindingInvalidID(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/repos/owner~repo/bindings/bad", nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{"name": "owner/repo", "id": "bad"})
	rec := httptest.NewRecorder()
	srv.handleDeleteBinding(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestHandleDeleteBindingCrossRepo verifies that a binding can only be deleted
// through its own repo's endpoint — trying to delete via a different repo's
// path must return 404, not silently succeed.
func TestHandleDeleteBindingCrossRepo(t *testing.T) {
	t.Parallel()
	srv, cleanup := openWriteTestServer(t)
	defer cleanup()

	// First add a second repo so we have two repos in the DB.
	putRepoReq := postJSON(t, http.MethodPut, "/api/repos",
		putRepoRequest{Name: "owner/other-repo", Enabled: true})
	srv.handlePutRepo(httptest.NewRecorder(), putRepoReq)

	// Add a binding to owner/repo.
	addReq := postJSON(t, http.MethodPost, "/api/repos/owner~repo/bindings",
		putBindingRequest{Agent: "coder", Cron: "0 * * * *"})
	addReq = muxVars(addReq, map[string]string{"name": "owner/repo"})
	addRec := httptest.NewRecorder()
	srv.handlePutBinding(addRec, addReq)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("add binding: want 201, got %d", addRec.Code)
	}
	var addResp putBindingResponse
	if err := json.NewDecoder(addRec.Body).Decode(&addResp); err != nil {
		t.Fatalf("decode add response: %v", err)
	}

	// Try to delete that binding via owner/other-repo — must return 404.
	delPath := "/api/repos/owner~other-repo/bindings/" + intStr(addResp.ID)
	req := httptest.NewRequest(http.MethodDelete, delPath, nil)
	req.Header.Set("Authorization", authHeader())
	req = muxVars(req, map[string]string{
		"name": "owner/other-repo",
		"id":   intStr(addResp.ID),
	})
	rec := httptest.NewRecorder()
	srv.handleDeleteBinding(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-repo delete: want 404, got %d", rec.Code)
	}

	// Verify the binding still exists under the correct repo.
	found := false
	for _, r := range srv.cfg().Repos {
		if r.Name == "owner/repo" {
			found = len(r.Use) > 0
		}
	}
	if !found {
		t.Error("binding was deleted despite cross-repo attempt")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// muxVars injects gorilla/mux route variables into a request for unit testing
// handlers that call mux.Vars(r).
func muxVars(r *http.Request, vars map[string]string) *http.Request {
	return mux.SetURLVars(r, vars)
}

// intStr converts an int64 to a decimal string.
func intStr(n int64) string {
	return strconv.FormatInt(n, 10)
}
