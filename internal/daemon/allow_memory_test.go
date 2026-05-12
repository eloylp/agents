package daemon_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestStoreCRUDAgentCreateDefaultsAllowMemoryTrue verifies that a POST that
// omits allow_memory persists the documented default, the GET response and
// the round-trip back through the wire shape both report true.
func TestStoreCRUDAgentCreateDefaultsAllowMemoryTrue(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST /agents: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/coder: got %d", rr.Code)
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AllowMemory == nil {
		t.Fatal("AllowMemory pointer should always be populated on GET, got nil")
	}
	if !*out.AllowMemory {
		t.Errorf("AllowMemory: got false, want true (default)")
	}
}

// TestStoreCRUDAgentCreateRoundTripsAllowMemoryFalse verifies that an explicit
// allow_memory=false payload survives the create-then-read cycle.
func TestStoreCRUDAgentCreateRoundTripsAllowMemoryFalse(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "stateless", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
		"allow_memory": false,
	}); rr.Code != http.StatusOK {
		t.Fatalf("POST /agents: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodGet, "/agents/stateless", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/stateless: got %d", rr.Code)
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AllowMemory == nil || *out.AllowMemory {
		t.Errorf("AllowMemory: got %v, want non-nil &false", out.AllowMemory)
	}
}

// TestStoreCRUDAgentPatchAllowMemoryFlipsWithoutAffectingOtherFields verifies
// the partial-update path: PATCH with only allow_memory must change just that
// field while leaving prompt, backend, skills, etc. untouched.
func TestStoreCRUDAgentPatchAllowMemoryFlipsWithoutAffectingOtherFields(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "coder", "backend": "claude", "model": "opus",
		"prompt": "p", "description": "d",
		"skills": []string{}, "can_dispatch": []string{},
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed coder: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/coder", map[string]any{
		"allow_memory": false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /agents/coder: got %d, %s", rr.Code, rr.Body.String())
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AllowMemory == nil || *out.AllowMemory {
		t.Errorf("AllowMemory after patch: got %v, want non-nil &false", out.AllowMemory)
	}
	// Other fields must be preserved, patching one toggle should not
	// disturb prompt_ref, model, description, or backend.
	if out.Backend != "claude" || out.Model != "opus" || out.Prompt != "" || out.PromptRef != "coder" || out.Description != "d" {
		t.Errorf("non-patched fields drifted: %+v", out)
	}
}

// TestStoreCRUDAgentPatchAllowMemoryTrueResetsExplicitFalse verifies the
// reverse path: an agent currently disabled can be flipped back on through a
// single PATCH so operators can revert a change without re-sending the whole
// agent.
func TestStoreCRUDAgentPatchAllowMemoryTrueResetsExplicitFalse(t *testing.T) {
	t.Parallel()
	s := openCRUDTestServer(t)
	seedStoreBackend(t, s, "claude")

	if rr := doCRUDRequest(t, s, http.MethodPost, "/agents", map[string]any{
		"name": "stateless", "backend": "claude", "prompt": "p",
		"skills": []string{}, "can_dispatch": []string{},
		"allow_memory": false,
	}); rr.Code != http.StatusOK {
		t.Fatalf("seed: got %d, %s", rr.Code, rr.Body.String())
	}

	rr := doCRUDRequest(t, s, http.MethodPatch, "/agents/stateless", map[string]any{
		"allow_memory": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /agents/stateless: got %d, %s", rr.Code, rr.Body.String())
	}
	var out storeAgentJSON
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AllowMemory == nil || !*out.AllowMemory {
		t.Errorf("AllowMemory after re-enabling patch: got %v, want non-nil &true", out.AllowMemory)
	}
}
