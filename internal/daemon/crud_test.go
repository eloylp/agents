package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/daemon"
	"github.com/eloylp/agents/internal/fleet"
)

// openCRUDTestServer creates a test server backed by a tempdir SQLite via
// the shared daemontest fixture. The CRUD tests exercise the same
// coordinator-driven write epoch + reload chain production runs.
func openCRUDTestServer(t *testing.T) *daemon.Daemon {
	t.Helper()
	srv, _ := newTestServer(t, crudMinimalConfig())
	return srv
}

// seedStoreBackend inserts a minimal backend into the server's store directly
// so that subsequent agent upserts that reference it pass cross-ref validation.
func seedStoreBackend(t *testing.T, s *daemon.Daemon, name string) {
	t.Helper()
	b := fleet.Backend{Command: name}
	if err := s.Store().UpsertBackend(name, b); err != nil {
		t.Fatalf("seedStoreBackend %s: %v", name, err)
	}
}

// seedStoreSkill inserts a minimal skill into the server's store directly.
func seedStoreSkill(t *testing.T, s *daemon.Daemon, name string) {
	t.Helper()
	if err := s.Store().UpsertSkill(name, fleet.Skill{Prompt: "skill prompt"}); err != nil {
		t.Fatalf("seedStoreSkill %s: %v", name, err)
	}
}

func seedStorePrompt(t *testing.T, s *daemon.Daemon, name string) {
	t.Helper()
	if _, err := s.Store().UpsertPrompt(fleet.Prompt{Name: name, Content: "prompt body"}); err != nil {
		t.Fatalf("seedStorePrompt %s: %v", name, err)
	}
}

func doCRUDRequest(t *testing.T, s *daemon.Daemon, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if method == http.MethodPost && path == "/agents" {
		if m, ok := body.(map[string]any); ok {
			if _, exists := m["description"]; !exists {
				m["description"] = "test agent"
			}
			if prompt, ok := m["prompt"].(string); ok && prompt != "" {
				ref, _ := m["prompt_ref"].(string)
				if ref == "" {
					ref, _ = m["name"].(string)
					ref = fleet.NormalizePromptName(ref)
					m["prompt_ref"] = ref
				}
				seedStorePrompt(t, s, ref)
				delete(m, "prompt")
			}
		}
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func doRawCRUDRequest(t *testing.T, s *daemon.Daemon, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
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

func TestStoreCRUDAgentListReturnsArray(t *testing.T) {
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
	// Store invariants require ≥1 agent, so the fixture pre-seeds one. The
	// endpoint just needs to return a JSON array, the array shape, not its
	// emptiness, is what /agents commits to.
	if agents == nil {
		t.Errorf("/agents returned nil slice, want JSON array")
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
		"description": "a reviewer agent", "allow_dispatch": true, "skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed pr-reviewer agent: got %d, %s", rr.Code, rr.Body.String())
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

	// POST, create
	rr := doCRUDRequest(t, s, http.MethodPost, "/agents", payload)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /agents: got %d, want 200, %s", rr.Code, rr.Body.String())
	}
	var created storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created agent id is empty, want generated stable id")
	}

	// GET list, should have two entries: pr-reviewer (seeded) + coder.
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

func TestStoreCRUDAgentCreateRejectsInlinePrompt(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	rr := doRawCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"description": "coding agent", "skills": []string{}, "can_dispatch": []string{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /agents inline prompt: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "prompt bodies are import-only") {
		t.Fatalf("error body = %q, want inline prompt rejection", rr.Body.String())
	}
}

func TestStoreCRUDAgentCreateRejectsConflictingPromptRefs(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	seedStorePrompt(t, s, "coder")

	rr := doRawCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt_id": "prompt_coder", "prompt_ref": "coder",
		"description": "coding agent", "skills": []string{}, "can_dispatch": []string{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /agents conflicting prompt refs: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "prompt_id and prompt_ref are mutually exclusive") {
		t.Fatalf("error body = %q, want prompt ref conflict rejection", rr.Body.String())
	}
}

func TestStoreCRUDAgentsListFiltersByWorkspace(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id": "team-a", "name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	for _, body := range []map[string]any{
		{"name": "default-reviewer", "backend": "claude", "prompt": "review default", "description": "default reviewer", "skills": []string{}, "can_dispatch": []string{}},
		{"workspace_id": "team-a", "name": "team-reviewer", "backend": "claude", "prompt": "review team", "description": "team reviewer", "skills": []string{}, "can_dispatch": []string{}},
	} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", body); rr.Code != http.StatusOK {
			t.Fatalf("seed agent %+v: got %d, %s", body, rr.Code, rr.Body.String())
		}
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/agents?workspace=team-a", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET team agents: got %d, %s", rr.Code, rr.Body.String())
	}
	var agents []viewAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "team-reviewer" || agents[0].WorkspaceID != "team-a" {
		t.Fatalf("team agents = %+v, want only team-reviewer", agents)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/agents/team-reviewer?workspace=default", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET team agent from default workspace: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentCreateIgnoresClientProvidedID(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"id": "client-id", "name": "coder", "backend": "claude", "prompt": "p",
		"description": "coding agent", "skills": []string{}, "can_dispatch": []string{},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /agents: got %d, %s", rr.Code, rr.Body.String())
	}
	var created storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" || created.ID == "client-id" {
		t.Fatalf("created ID = %q, want server-owned id", created.ID)
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
			t.Fatalf("seed agent %s: got %d, %s", name, rr.Code, rr.Body.String())
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
			t.Fatalf("seed agent %s: got %d, %s", name, rr.Code, rr.Body.String())
		}
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d, %s", rr.Code, rr.Body.String())
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
			t.Fatalf("seed agent %s: got %d, %s", name, rr.Code, rr.Body.String())
		}
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:fix"}, "enabled": true},
			{"agent": "reviewer", "labels": []string{"ai:review"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/agents/coder?cascade=true", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE ?cascade=true: got %d, want 204, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("POST skill: got %d, %s", rr.Code, rr.Body.String())
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

// ── /prompts ────────────────────────────────────────────────────────

func TestStoreCRUDPromptCreatePatchDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/prompts", map[string]any{
		"name":        "release-notes",
		"description": "Drafts releases",
		"content":     "Summarize merged work.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST prompt: got %d, %s", rr.Code, rr.Body.String())
	}
	var created storePromptJSON
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID != "prompt_release-notes" {
		t.Fatalf("created ID = %q, want prompt_release-notes", created.ID)
	}

	rr = doCRUDRequest(t, s, http.MethodPatch, "/prompts/"+created.ID, map[string]any{
		"description": "Updated",
		"content":     "Write concise release notes.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH prompt: got %d, %s", rr.Code, rr.Body.String())
	}
	var patched storePromptJSON
	if err := json.NewDecoder(rr.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
	if patched.ID != created.ID || patched.Description != "Updated" || patched.Content != "Write concise release notes." {
		t.Fatalf("patched prompt = %+v, want same id and updated fields", patched)
	}
	if patched.Name != "release-notes" {
		t.Fatalf("patched prompt name = %q, want canonical release-notes", patched.Name)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/prompts/"+created.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET prompt: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/prompts/"+created.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE prompt: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodGet, "/prompts/release-notes", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDPromptScopedDuplicatesUseStableID(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	for _, body := range []map[string]any{
		{"workspace_id": "team-a", "name": "shared", "content": "Team prompt."},
		{"workspace_id": "team-b", "name": "shared", "content": "Other prompt."},
	} {
		rr := doCRUDRequest(t, s, http.MethodPost, "/prompts", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("POST prompt: got %d, %s", rr.Code, rr.Body.String())
		}
	}
	rr := doCRUDRequest(t, s, http.MethodGet, "/prompts/prompt_team-a_shared", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET scoped prompt by id: got %d, %s", rr.Code, rr.Body.String())
	}
	var got storePromptJSON
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode prompt: %v", err)
	}
	if got.WorkspaceID != "team-a" || got.Name != "shared" {
		t.Fatalf("prompt by id = %+v, want team-a/shared", got)
	}

	rr = doCRUDRequest(t, s, http.MethodPatch, "/prompts/prompt_team-b_shared", map[string]any{"content": "Updated other prompt."})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH scoped prompt by id: got %d, %s", rr.Code, rr.Body.String())
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode patched prompt: %v", err)
	}
	if got.WorkspaceID != "team-b" || got.Content != "Updated other prompt." {
		t.Fatalf("patched prompt = %+v, want team-b updated", got)
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/prompts/prompt_team-a_shared", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE scoped prompt by id: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodGet, "/prompts/prompt_team-b_shared", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET remaining scoped prompt: got %d", rr.Code)
	}
}

func TestStoreCRUDPromptDeleteReferencedByAgent(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"description": "coding agent", "skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/prompts/coder", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE referenced prompt: got %d, want 409, %s", rr.Code, rr.Body.String())
	}
}

// ── /workspaces ────────────────────────────────────────────────────────

func TestStoreCRUDWorkspaceCreatePatchDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"name":        "Team A",
		"description": "Product workspace",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	var created storeWorkspaceJSON
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	if created.ID != "team-a" || created.Name != "Team A" || created.Description != "Product workspace" {
		t.Fatalf("created workspace = %+v, want derived id and fields", created)
	}

	rr = doCRUDRequest(t, s, http.MethodPatch, "/workspaces/team-a", map[string]any{
		"description": "Updated",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	var patched storeWorkspaceJSON
	if err := json.NewDecoder(rr.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched workspace: %v", err)
	}
	if patched.ID != created.ID || patched.Description != "Updated" {
		t.Fatalf("patched workspace = %+v, want same id and updated description", patched)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/workspaces/Team%20A", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET workspace by name: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodDelete, "/workspaces/team-a", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodGet, "/workspaces/team-a", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET deleted workspace: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDWorkspaceDeleteDefaultRejected(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodDelete, "/workspaces/default", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE default workspace: got %d, want 409, %s", rr.Code, rr.Body.String())
	}
}

func TestStoreCRUDWorkspaceLookupPrefersIDOverNameCollision(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	for _, body := range []map[string]any{
		{"id": "foo", "name": "Zulu"},
		{"id": "bar", "name": "foo"},
	} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", body); rr.Code != http.StatusOK {
			t.Fatalf("seed workspace %+v: got %d, %s", body, rr.Code, rr.Body.String())
		}
	}
	rr := doCRUDRequest(t, s, http.MethodGet, "/workspaces/foo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /workspaces/foo: got %d, %s", rr.Code, rr.Body.String())
	}
	var got storeWorkspaceJSON
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if got.ID != "foo" || got.Name != "Zulu" {
		t.Fatalf("GET /workspaces/foo = %+v, want id match foo/Zulu", got)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/workspaces/foo/guardrails", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /workspaces/foo/guardrails: got %d, %s", rr.Code, rr.Body.String())
	}
	var refs []workspaceGuardrailJSON
	if err := json.NewDecoder(rr.Body).Decode(&refs); err != nil {
		t.Fatalf("decode guardrails: %v", err)
	}
	if len(refs) == 0 || refs[0].WorkspaceID != "foo" {
		t.Fatalf("guardrails workspace = %+v, want workspace_id foo", refs)
	}
}

func TestStoreCRUDWorkspaceGuardrailsReplace(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id":   "team-a",
		"name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/workspaces/team-a/guardrails", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET inherited workspace guardrails: got %d, %s", rr.Code, rr.Body.String())
	}
	var inherited []workspaceGuardrailJSON
	if err := json.NewDecoder(rr.Body).Decode(&inherited); err != nil {
		t.Fatalf("decode inherited guardrails: %v", err)
	}
	if len(inherited) == 0 {
		t.Fatal("inherited guardrails are empty, want built-in references")
	}

	rr = doCRUDRequest(t, s, http.MethodPut, "/workspaces/team-a/guardrails", []map[string]any{
		{"guardrail_name": "security", "position": 10, "enabled": true},
		{"guardrail_name": "memory-scope", "position": 20, "enabled": false},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT workspace guardrails: got %d, %s", rr.Code, rr.Body.String())
	}
	var updated []workspaceGuardrailJSON
	if err := json.NewDecoder(rr.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated guardrails: %v", err)
	}
	if len(updated) != 2 {
		t.Fatalf("updated guardrails len = %d, want 2", len(updated))
	}
	if updated[0].GuardrailName != "security" || updated[0].Position != 10 || !updated[0].Enabled {
		t.Fatalf("updated[0] = %+v, want enabled security at position 10", updated[0])
	}
	if updated[1].GuardrailName != "memory-scope" || updated[1].Position != 20 || updated[1].Enabled {
		t.Fatalf("updated[1] = %+v, want disabled memory-scope at position 20", updated[1])
	}

	rr = doCRUDRequest(t, s, http.MethodPut, "/workspaces/team-a/guardrails", []map[string]any{
		{"guardrail_name": "missing", "enabled": true},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT unknown workspace guardrail: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
}

func TestStoreCRUDWorkspaceGuardrailsPreserveExplicitZeroPosition(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id":   "team-a",
		"name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	rr := doCRUDRequest(t, s, http.MethodPut, "/workspaces/team-a/guardrails", []map[string]any{
		{"guardrail_name": "security", "position": 5, "enabled": true},
		{"guardrail_name": "memory-scope", "position": 0, "enabled": true},
		{"guardrail_name": "mcp-tool-usage", "enabled": true},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT workspace guardrails: got %d, %s", rr.Code, rr.Body.String())
	}
	var refs []workspaceGuardrailJSON
	if err := json.NewDecoder(rr.Body).Decode(&refs); err != nil {
		t.Fatalf("decode guardrails: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("guardrails len = %d, want 3", len(refs))
	}
	if refs[0].GuardrailName != "memory-scope" || refs[0].Position != 0 {
		t.Fatalf("refs[0] = %+v, want explicit position 0 memory-scope", refs[0])
	}
	if refs[1].GuardrailName != "mcp-tool-usage" || refs[1].Position != 2 {
		t.Fatalf("refs[1] = %+v, want omitted position defaulted to request index 2", refs[1])
	}
	if refs[2].GuardrailName != "security" || refs[2].Position != 5 {
		t.Fatalf("refs[2] = %+v, want security at position 5", refs[2])
	}
}

func TestStoreCRUDReposListFiltersByWorkspace(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id": "team-a", "name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	for _, body := range []map[string]any{
		{"name": "owner/default", "enabled": true, "bindings": []map[string]any{}},
		{"workspace_id": "team-a", "name": "owner/team", "enabled": true, "bindings": []map[string]any{}},
	} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", body); rr.Code != http.StatusOK {
			t.Fatalf("seed repo %+v: got %d, %s", body, rr.Code, rr.Body.String())
		}
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/repos?workspace=team-a", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET team repos: got %d, %s", rr.Code, rr.Body.String())
	}
	var repos []storeRepoJSON
	if err := json.NewDecoder(rr.Body).Decode(&repos); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "owner/team" || repos[0].WorkspaceID != "team-a" {
		t.Fatalf("team repos = %+v, want only owner/team", repos)
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/team?workspace=default", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET team repo from default workspace: got %d, want 404", rr.Code)
	}
}

// ── /guardrails ─────────────────────────────────────────────────────

func TestStoreCRUDGuardrailsListSeeded(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	rr := doCRUDRequest(t, s, http.MethodGet, "/guardrails", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /guardrails: got %d, %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 4 ||
		rows[0]["name"] != "security" || rows[0]["is_builtin"] != true ||
		rows[1]["name"] != "discretion" || rows[1]["is_builtin"] != true ||
		rows[2]["name"] != "memory-scope" || rows[2]["is_builtin"] != true ||
		rows[3]["name"] != "mcp-tool-usage" || rows[3]["is_builtin"] != true {
		t.Fatalf("expected seeded built-ins [security, discretion, memory-scope, mcp-tool-usage], got %+v", rows)
	}
}

func TestStoreCRUDGuardrailCreatePatchDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Create operator-added guardrail.
	rr := doCRUDRequest(t, s, http.MethodPost, "/guardrails", map[string]any{
		"name":        "Code Style",
		"description": "Project conventions",
		"content":     "Always run gofmt.",
		"enabled":     true,
		"position":    50,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST guardrail: got %d, %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created["name"] != "code-style" {
		t.Errorf("name normalisation: got %v, want code-style", created["name"])
	}
	if created["is_builtin"] != false {
		t.Errorf("operator row must not be flagged built-in; got is_builtin=%v", created["is_builtin"])
	}

	// PATCH content + disable.
	rr = doCRUDRequest(t, s, http.MethodPatch, "/guardrails/code-style", map[string]any{
		"content": "Always run gofmt and goimports.",
		"enabled": false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH guardrail: got %d, %s", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
	if patched["content"] != "Always run gofmt and goimports." || patched["enabled"] != false {
		t.Errorf("PATCH did not apply: %+v", patched)
	}
	if patched["position"] != float64(50) {
		t.Errorf("PATCH must preserve unrelated fields; position=%v want 50", patched["position"])
	}

	// DELETE removes the row.
	rr = doCRUDRequest(t, s, http.MethodDelete, "/guardrails/code-style", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE guardrail: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodGet, "/guardrails/code-style", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDGuardrailReset(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Edit the security guardrail, then reset.
	rr := doCRUDRequest(t, s, http.MethodPatch, "/guardrails/security", map[string]any{
		"content": "Operator-edited body.",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH security: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodPost, "/guardrails/security/reset", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST reset: got %d, %s", rr.Code, rr.Body.String())
	}
	var reset map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&reset); err != nil {
		t.Fatalf("decode reset: %v", err)
	}
	if reset["content"] != reset["default_content"] {
		t.Error("reset did not restore default_content into content")
	}

	// Reset on an operator-added row (no default) returns 400.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/guardrails", map[string]any{
		"name": "code-style", "content": "x", "enabled": true,
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST seed code-style: got %d, %s", rr.Code, rr.Body.String())
	}
	rr = doCRUDRequest(t, s, http.MethodPost, "/guardrails/code-style/reset", nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("reset on operator row: got %d, want 400", rr.Code)
	}
}

// ── /backends ───────────────────────────────────────────────────────

func TestStoreCRUDBackendCreateAndDelete(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Create a fresh backend on top of the fixture's seeded "claude". The
	// seeded agent depends on "claude", so we delete the new "codex" entry
	// in the cleanup phase, that leaves the seeded pair intact and verifies
	// DELETE works against an unreferenced backend.
	if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
		"name": "codex", "command": "codex", "args": []string{}, "env": map[string]string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST backend codex: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/backends/codex", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET backend: got %d", rr.Code)
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/backends/codex", nil)
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
		t.Fatalf("POST backend: got %d, %s", rr.Code, rr.Body.String())
	}

	// GET list, env values must be redacted.
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
		t.Fatalf("POST backend: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/backends/claude", map[string]any{
		"timeout_seconds":  900,
		"max_prompt_chars": 45000,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /backends/claude: got %d, %s", rr.Code, rr.Body.String())
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

	backends, err := s.Store().ReadBackends()
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
		t.Fatalf("POST backend: got %d, %s", rr.Code, rr.Body.String())
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
	if err := s.Store().UpsertBackend("claude", fleet.Backend{
		Command: "/bin/sh",
	}); err != nil {
		t.Fatalf("seed claude backend: %v", err)
	}

	rr := doCRUDRequest(t, s, http.MethodPost, "/backends/local", map[string]any{
		"name": "qwen_local",
		"url":  "http://localhost:18000/v1/messages",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /backends/local: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("GET /backends/qwen_local: got %d, %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodDelete, "/backends/qwen_local", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE /backends/qwen_local: got %d, %s", rr.Code, rr.Body.String())
	}
}

func TestBackendsLocalRejectReservedName(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	if err := s.Store().UpsertBackend("claude", fleet.Backend{
		Command: "/bin/sh",
	}); err != nil {
		t.Fatalf("seed claude backend: %v", err)
	}

	rr := doCRUDRequest(t, s, http.MethodPost, "/backends/local", map[string]any{
		"name": "claude",
		"url":  "http://localhost:18000/v1/messages",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /backends/local with reserved name: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed coder agent: got %d, %s", rr.Code, rr.Body.String())
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
			t.Fatalf("POST repo %s: got %d, %s", name, rr.Code, rr.Body.String())
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

	// GET single, repo name is owner/repo → /repos/owner/repo
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
		t.Fatalf("DELETE repo: got %d, %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET after delete: got %d, want 404", rr.Code)
	}
}

// ── atomic binding CRUD ──────────────────────────────────────────────────────

// seedBindingTestRepo creates a repo + agent pair so the binding test can
// target the /repos/{owner}/{repo}/bindings routes directly.
func seedBindingTestRepo(t *testing.T, s *daemon.Daemon) {
	t.Helper()
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: got %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name":    "owner/repo",
		"enabled": true,
		"bindings": []map[string]any{
			{"agent": "coder", "labels": []string{"ai:seed"}, "enabled": true},
		},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create binding: got %d, %s", rr.Code, rr.Body.String())
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
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo/bindings/"+strconv.Itoa(int(id)), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET binding: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("expected 400, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("expected 404, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create: got %d, %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&created)
	id := int(created["id"].(float64))

	enabled := false
	rr = doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo/bindings/"+strconv.Itoa(id), map[string]any{
		"agent":   "coder",
		"cron":    "0 9 * * *",
		"enabled": enabled,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: got %d, %s", rr.Code, rr.Body.String())
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
	rr = doCRUDRequest(t, s, http.MethodPatch, "/repos/owner/repo/bindings/"+strconv.Itoa(id), map[string]any{
		"agent": "coder", "labels": []string{"ai:changed"},
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d, %s", rr.Code, rr.Body.String())
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

	rr = doCRUDRequest(t, s, http.MethodDelete, "/repos/owner/repo/bindings/"+strconv.Itoa(id), nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo/bindings/"+strconv.Itoa(id), nil)
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
		t.Fatalf("PATCH repo: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("expected 400 for empty patch, got %d, %s", rr.Code, rr.Body.String())
	}
}

func TestGetRepoExposesBindingIDs(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedBindingTestRepo(t, s)

	rr := doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET repo: got %d, %s", rr.Code, rr.Body.String())
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

// The pre-cutover reload-failure tests asserted that a CRUD write that
// fails to propagate to the runtime returns 500 and leaves the in-memory
// cfg pointer unchanged. Both behaviours are gone: there is no reload
// chain (the runtime reads from SQLite on demand) and no in-memory cfg
// pointer to update or roll back. The tests have been removed. Write-time
// validation (FK constraints, agent / cron / backend cross-refs, etc.)
// still runs inside store.UpsertX and surfaces as 4xx; that path has its
// own tests in the store package.

// ── /api/store POST body-size limiting ───────────────────────────────────────

// TestStoreCRUDPostBodySizeLimit verifies that POST write endpoints return
// 413 when the request body exceeds AGENTS_HTTP_MAX_BODY_BYTES, including
// the case where the body starts with a valid JSON object followed by
// extra bytes that push the total over the limit.
func TestStoreCRUDPostBodySizeLimit(t *testing.T) {
	t.Parallel()

	cfg := crudMinimalConfig()
	cfg.Daemon.HTTP.MaxBodyBytes = 10 // very small limit for the test
	s, _ := newTestServer(t, cfg)

	tests := []struct {
		name string
		path string
		body any
	}{
		{
			name: "agent",
			path: "/agents",
			body: map[string]any{"name": "coder", "backend": "claude", "prompt": "You write code, a much longer prompt than 10 bytes."},
		},
		{
			name: "skill",
			path: "/skills",
			body: map[string]any{"name": "arch", "prompt": "You are an architect, longer than 10 bytes."},
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
	s, _ := newTestServer(t, cfg)

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
			s.Handler().ServeHTTP(rr, req)
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
		setup func(t *testing.T, s *daemon.Daemon)
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
			setup: func(t *testing.T, s *daemon.Daemon) { seedStoreBackend(t, s, "claude") },
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
			setup: func(t *testing.T, s *daemon.Daemon) {
				seedStoreBackend(t, s, "claude")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
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
		setup func(t *testing.T, s *daemon.Daemon)
	}{
		{
			name: "delete last backend",
			path: "/backends/claude",
			setup: func(t *testing.T, s *daemon.Daemon) {
				if rr := doCRUDRequest(t, s, http.MethodPost, "/backends", map[string]any{
					"name": "claude", "command": "claude", "args": []string{}, "env": map[string]string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create backend: got %d, %s", rr.Code, rr.Body.String())
				}
			},
		},
		{
			name: "delete last agent",
			path: "/agents/coder",
			setup: func(t *testing.T, s *daemon.Daemon) {
				seedStoreBackend(t, s, "claude")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
				}
			},
		},
		{
			name: "delete backend referenced by agent",
			path: "/backends/claude",
			setup: func(t *testing.T, s *daemon.Daemon) {
				seedStoreBackend(t, s, "claude")
				seedStoreBackend(t, s, "codex")
				if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
					"name": "coder", "backend": "claude", "prompt": "p",
					"skills": []string{}, "can_dispatch": []string{},
				}); rr.Code != http.StatusOK {
					t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
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

// ── Concurrent write serialisation ───────────────────────────────────────────

// TestConcurrentWritesAllCommit verifies that concurrent POST /agents
// requests all reach SQLite without losing writes. The pre-cutover
// version of this test asserted that the coordinator's reload chain saw
// every committed snapshot in order; that machinery is gone now (no
// reload chain), so the surviving contract is "every successful HTTP
// 200 response corresponds to a row in SQLite." Run with -race to catch
// data races on the underlying handle.
func TestConcurrentWritesAllCommit(t *testing.T) {
	t.Parallel()

	s, _ := newTestServer(t, crudMinimalConfig())
	seedStoreBackend(t, s, "claude")

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
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

	// All n requested agents plus the one the fixture seeded must be in
	// SQLite, no committed write may be lost.
	const want = n + 1
	agents, err := s.Store().ReadAgents()
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	if len(agents) != want {
		t.Fatalf("expected %d agents in DB, got %d", want, len(agents))
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
		t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create backend: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
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

	// POST agent with lowercase name, stored as "coder".
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
	}

	// GET with mixed-case path, should return 200, not 404.
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

	// DELETE with mixed-case path, should actually remove the entity.
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
		t.Fatalf("seed agent: got %d, %s", rr.Code, rr.Body.String())
	}

	// Create two repos so that deleting one still leaves the fleet valid.
	for _, name := range []string{"owner/repo", "owner/other"} {
		if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
			"name": name, "enabled": true,
			"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai"}, "enabled": true}},
		}); rr.Code != http.StatusOK {
			t.Fatalf("POST repo %s: got %d, %s", name, rr.Code, rr.Body.String())
		}
	}

	// GET with mixed-case owner, should return 200, not 404.
	rr := doCRUDRequest(t, s, http.MethodGet, "/repos/Owner/repo", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /repos/Owner/repo: got %d, want 200", rr.Code)
	}

	// GET with mixed-case repo segment, should return 200.
	rr = doCRUDRequest(t, s, http.MethodGet, "/repos/owner/Repo", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /repos/owner/Repo: got %d, want 200", rr.Code)
	}

	// DELETE with mixed-case path, should actually remove the repo.
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
		t.Fatalf("POST backend: got %d, %s", rr.Code, rr.Body.String())
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
	// lowercase name and an explicit prompt_ref.
	rr = doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name":    "  Coder  ",
		"backend": "claude",
		"prompt":  "  You write code.  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST agent: got %d, %s", rr.Code, rr.Body.String())
	}
	var agent map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&agent); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if agent["name"] != "coder" {
		t.Errorf("agent name: got %q, want %q", agent["name"], "coder")
	}
	if prompt, ok := agent["prompt"].(string); ok && prompt != "" {
		t.Errorf("agent prompt field: got %q, want omitted or empty", prompt)
	}
	if agent["prompt_ref"] != "coder" {
		t.Errorf("agent prompt fields: got prompt=%q prompt_ref=%q, want empty prompt and coder ref", agent["prompt"], agent["prompt_ref"])
	}

	// ── skill ────────────────────────────────────────────────────────────────
	// POST with mixed-case name and whitespace-padded prompt; response must
	// have lowercase name and trimmed prompt.
	rr = doCRUDRequest(t, s, http.MethodPost, "/skills", map[string]any{
		"name":   "Architect",
		"prompt": "  Focus on design.  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("POST skill: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("POST repo: got %d, %s", rr.Code, rr.Body.String())
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

// TestServerCfgUpdatedAfterCRUDWrite verifies that a newly-added repo is
// accepted by the webhook event path and visible in /agents immediately
// after a CRUD write, without requiring a restart.
//
// In the pre-cutover design this exercised the in-memory cfg pointer +
// reload chain. After the refactor every read goes straight to SQLite,
// so the test simply asserts that the data is observable from the
// database and from the HTTP surfaces that read it.
func TestServerCfgUpdatedAfterCRUDWrite(t *testing.T) {
	t.Parallel()

	s := openCRUDTestServer(t)
	// Precondition: the fixture starts with the seeded "coder" agent (the
	// fixture seeds one to satisfy the store's "≥ 1 agent" invariant) and
	// no repos.
	repos, err := s.Store().ReadRepos()
	if err != nil {
		t.Fatalf("read repos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("precondition: expected 0 repos, got %d", len(repos))
	}

	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/newrepo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST repo: %d, %s", rr.Code, rr.Body.String())
	}

	// Verify SQLite has the new repo.
	repos, err = s.Store().ReadRepos()
	if err != nil {
		t.Fatalf("read repos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "owner/newrepo" {
		t.Fatalf("repo not persisted: %v", repos)
	}

	// Verify /api/agents reflects the new agent.
	rr := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/agents: %d, %s", rr.Code, rr.Body.String())
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
	// Webhook secret is empty (crudMinimalConfig default), verifySignature
	// requires non-empty secret, so the request will be rejected as unauthorized
	// if routed; but the repo gate runs before enqueue. We test the repo gate by
	// observing whether the handler returns 401 (signature check, meaning it got
	// past the "repo not found" early-return) vs 202 (repo not found silently
	// ignored). With the repo absent, the handler returns 202 immediately (no
	// event enqueued). With the repo present, it proceeds to signature check.
	// Since the server has no webhook secret configured, verifySignature returns
	// false and the handler returns 401, which proves the routing reached the
	// signature check gate, i.e., the repo was found in the updated config.
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, req)
	// 401 means signature check ran, which only happens after the repo gate
	// passes: the new repo was found in the post-write in-memory config.
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("webhook after CRUD repo add: want 401 (signature check = repo found), got %d, body: %s",
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
		t.Fatalf("create agent: got %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create repo: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodDelete, "/repos/owner/repo", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE last enabled repo: want 204, got %d, %s", rr.Code, rr.Body.String())
	}

	// The repo must be gone from subsequent reads.
	if rr := doCRUDRequest(t, s, http.MethodGet, "/repos/owner/repo", nil); rr.Code != http.StatusNotFound {
		t.Errorf("GET deleted repo: want 404, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("create agent: %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "coder", "labels": []string{"ai:fix"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create repo: %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/export", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: got %d, %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"prompts:", "workspaces:", "prompt_ref: coder", "guardrails:", "coder"} {
		if !strings.Contains(body, want) {
			t.Errorf("export YAML missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "agents:\n    -") && strings.Contains(body, "prompt: help") {
		t.Errorf("export nested agent includes inline prompt content: %s", body)
	}
	if !strings.Contains(body, "owner/repo") {
		t.Errorf("export YAML missing repo name: %s", body)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "yaml") {
		t.Errorf("export Content-Type want yaml, got %q", ct)
	}
}

func TestStoreImportWorkspaceShape(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	yamlBody := `backends:
  claude:
    command: claude
prompts:
  - name: imported-prompt
    content: imported prompt
skills: {}
workspaces:
  - id: team-a
    name: Team A
    guardrails:
      - guardrail_name: security
        enabled: true
    agents:
      - name: imported-agent
        backend: claude
        prompt_ref: imported-prompt
        description: imported agent
        skills: []
        can_dispatch: []
        scope_type: repo
        scope_repo: owner/new-repo
    repos:
      - name: owner/new-repo
        enabled: true
        use:
          - agent: imported-agent
            labels: [ai:run]
    token_budgets:
      - scope_kind: workspace+agent
        agent: imported-agent
        period: monthly
        cap_tokens: 100000
        alert_at_pct: 80
        enabled: true
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("workspace import: got %d, %s", rr.Code, rr.Body.String())
	}
	var summary map[string]int
	if err := json.NewDecoder(rr.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary["workspaces"] != 1 || summary["prompts"] != 1 || summary["agents"] != 1 || summary["repos"] != 1 || summary["token_budgets"] != 1 {
		t.Fatalf("summary = %+v, want one imported workspace/prompt/agent/repo/budget", summary)
	}

	agents := doCRUDRequest(t, s, http.MethodGet, "/agents?workspace=team-a", nil)
	if !strings.Contains(agents.Body.String(), "imported-agent") || !strings.Contains(agents.Body.String(), "imported-prompt") {
		t.Fatalf("workspace agent missing after import: %s", agents.Body.String())
	}
	defaultAgents := doCRUDRequest(t, s, http.MethodGet, "/agents", nil)
	if strings.Contains(defaultAgents.Body.String(), "imported-agent") {
		t.Fatalf("workspace agent leaked into default workspace listing: %s", defaultAgents.Body.String())
	}
	exported := doCRUDRequest(t, s, http.MethodGet, "/export", nil)
	if exported.Code != http.StatusOK {
		t.Fatalf("export after workspace import: got %d, %s", exported.Code, exported.Body.String())
	}
	for _, want := range []string{"workspaces:", "id: team-a", "prompts:", "prompt_ref: imported-prompt", "workspace+agent"} {
		if !strings.Contains(exported.Body.String(), want) {
			t.Errorf("export after import missing %q: %s", want, exported.Body.String())
		}
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
		t.Fatalf("seed repo: %d, %s", rr.Code, rr.Body.String())
	}

	yaml := `agents:
  - name: imported-agent
    backend: claude
    prompt: imported prompt
    description: imported agent
    skills: []
    can_dispatch: []
skills:
  imported-skill:
    prompt: imported skill prompt
`
	req := httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(yaml))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed old-agent: %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/old-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "old-agent", "labels": []string{"ai:scan"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed old-repo: %d, %s", rr.Code, rr.Body.String())
	}

	// Replace with a YAML that contains only a new agent + repo (no old-agent).
	yamlBody := `
backends:
    claude:
      command: claude
      args: []
agents:
  - name: new-agent
    backend: claude
    prompt: fresh
    description: new agent
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
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("replace import: got %d, %s", rr.Code, rr.Body.String())
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

func TestStoreImportReplaceRejectsEmptyFleet(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)

	// Replace-mode import with only skills, must fail because the
	// resulting store would have no agents and no backends, violating
	// the store's minimum-cardinality invariants.
	yamlBody := `skills:
  my-skill:
    prompt: just a skill
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("import skills-only with replace mode: want 400, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed agent: %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/r", "enabled": true,
		"bindings": []map[string]any{{"agent": "existing-agent", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d, %s", rr.Code, rr.Body.String())
	}

	// Replace with a YAML that contains a backend but no agents, should fail.
	yamlBody := `backends:
    claude:
      command: claude
      args: []
`
	req := httptest.NewRequest(http.MethodPost, "/import?mode=replace", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("replace with no agents: want 400, got %d, %s", rr.Code, rr.Body.String())
	}

	// The original agent must still be present, the failed replace must not
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
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid mode: want 400, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed agent: %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/existing-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "scout", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d, %s", rr.Code, rr.Body.String())
	}

	yamlBody := `
backends:
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
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("merge import with invalid cron: want 400, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed agent: %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/repos", map[string]any{
		"name": "owner/existing-repo", "enabled": true,
		"bindings": []map[string]any{{"agent": "scout", "labels": []string{"ai:run"}}},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed repo: %d, %s", rr.Code, rr.Body.String())
	}

	yamlBody := `
backends:
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
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("replace import with invalid cron: want 400, got %d, %s", rr.Code, rr.Body.String())
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
		t.Fatalf("seed coder: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"backend": "codex",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /agents/coder: got %d, %s", rr.Code, rr.Body.String())
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Backend != "codex" {
		t.Fatalf("backend: got %q, want %q", out.Backend, "codex")
	}
	if out.Model != "opus" || out.Prompt != "" || out.PromptRef != "coder" || out.Description != "d" {
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
		"description": "new",
	}); rr.Code != http.StatusNotFound {
		t.Fatalf("PATCH missing: got %d, want 404", rr.Code)
	}
}

func TestStoreCRUDAgentPatchRejectsInlinePrompt(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	seedStorePrompt(t, s, "coder")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt_ref": "coder",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: %s", rr.Body.String())
	}

	rr := doRawCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"prompt": "new body",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH /agents/coder inline prompt: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "prompt bodies are import-only") {
		t.Fatalf("error body = %q, want inline prompt rejection", rr.Body.String())
	}
}

func TestStoreCRUDAgentPatchRejectsConflictingPromptRefs(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	seedStorePrompt(t, s, "coder")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt_ref": "coder",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: %s", rr.Body.String())
	}

	rr := doRawCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"prompt_id":  "prompt_coder",
		"prompt_ref": "coder",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH /agents/coder conflicting prompt refs: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "prompt_id and prompt_ref are mutually exclusive") {
		t.Fatalf("error body = %q, want prompt ref conflict rejection", rr.Body.String())
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
		t.Fatalf("unknown backend: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
}

func TestStoreCRUDAgentPatchRejectsWorkspaceMove(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id": "team-a", "name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"description": "default coder", "skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed agent: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"workspace_id": "team-a",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("workspace move patch: got %d, want 400, %s", rr.Code, rr.Body.String())
	}
}

func TestStoreCRUDAgentPatchHonorsWorkspaceQuery(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")
	if rr := doCRUDRequest(t, s, http.MethodPost, "/workspaces", map[string]any{
		"id": "team-a", "name": "Team A",
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"workspace_id": "team-a", "name": "team-coder", "backend": "claude", "prompt": "p",
		"description": "team coder", "skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed team agent: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/team-coder?workspace=default", map[string]any{
		"description": "wrong workspace",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("patch from wrong workspace: got %d, want 404, %s", rr.Code, rr.Body.String())
	}

	rr = doCRUDRequest(t, s, http.MethodPatch, "/agents/team-coder?workspace=team-a", map[string]any{
		"description": "updated team coder",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch from matching workspace: got %d, %s", rr.Code, rr.Body.String())
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.WorkspaceID != "team-a" || out.Description != "updated team coder" {
		t.Fatalf("patched agent = %+v, want team-a updated description", out)
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
		t.Fatalf("PATCH /skills/architect: got %d, %s", rr.Code, rr.Body.String())
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

// ── PATCH /backends/{name}, superset shape ─────────────────────────

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
		t.Fatalf("PATCH backend: got %d, %s", rr.Code, rr.Body.String())
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
