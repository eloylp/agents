package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// TestToolCreateAgentForwardsAllowMemoryFalse verifies that the create_agent
// tool reads the boolean argument and forwards it to UpsertAgent as a non-nil
// *bool — without that the runtime gate cannot distinguish "user wants
// memory disabled" from the documented default.
func TestToolCreateAgentForwardsAllowMemoryFalse(t *testing.T) {
	t.Parallel()
	canonical := fleet.Agent{Name: "linter", Backend: "claude", Prompt: "audit"}
	w := &stubAgentWriter{canonical: canonical}
	deps := Deps{
		DB:         testDB(t),
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "linter",
		"backend":      "claude",
		"prompt":       "audit",
		"allow_memory": false,
	}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent: err=%v body=%s", err, textOf(t, res))
	}
	if w.gotUpsert.AllowMemory == nil {
		t.Fatal("AllowMemory pointer should be non-nil after explicit false in payload")
	}
	if *w.gotUpsert.AllowMemory {
		t.Errorf("AllowMemory: got &true, want &false")
	}
}

// TestToolCreateAgentLeavesAllowMemoryNilWhenAbsent verifies that omitting
// allow_memory leaves the AgentDef pointer nil, so AgentDef.IsAllowMemory()
// returns the documented default of true downstream.
func TestToolCreateAgentLeavesAllowMemoryNilWhenAbsent(t *testing.T) {
	t.Parallel()
	canonical := fleet.Agent{Name: "linter", Backend: "claude", Prompt: "audit"}
	w := &stubAgentWriter{canonical: canonical}
	deps := Deps{
		DB:         testDB(t),
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":    "linter",
		"backend": "claude",
		"prompt":  "audit",
	}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent: err=%v body=%s", err, textOf(t, res))
	}
	if w.gotUpsert.AllowMemory != nil {
		t.Errorf("AllowMemory: got %v, want nil when absent from payload", *w.gotUpsert.AllowMemory)
	}
}

// TestToolUpdateAgentForwardsAllowMemoryPatch verifies that update_agent
// surfaces allow_memory in the partial-update payload as a *bool — exactly
// what the webhook adapter expects to merge over an existing AgentDef.
func TestToolUpdateAgentForwardsAllowMemoryPatch(t *testing.T) {
	t.Parallel()
	canonical := fleet.Agent{Name: "coder", Backend: "claude", Prompt: "p"}
	w := &stubAgentWriter{patchCanonical: canonical}
	deps := Deps{
		DB:         testDB(t),
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "coder",
		"allow_memory": false,
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_agent: err=%v body=%s", err, textOf(t, res))
	}
	if w.gotPatch.AllowMemory == nil || *w.gotPatch.AllowMemory {
		t.Fatalf("AllowMemory patch: got %v, want non-nil &false", w.gotPatch.AllowMemory)
	}
	// Other fields untouched in this patch must remain nil.
	if w.gotPatch.Backend != nil || w.gotPatch.Prompt != nil {
		t.Errorf("unset fields should be nil, got backend=%v prompt=%v", w.gotPatch.Backend, w.gotPatch.Prompt)
	}
}

// TestToolUpdateAgentRejectsNonBooleanAllowMemory mirrors the existing
// boolean-arg validation: a typo like "yes" must fail cleanly rather than
// silently being treated as absent (which would mis-trigger preserve-semantics).
func TestToolUpdateAgentRejectsNonBooleanAllowMemory(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{}
	deps := Deps{
		DB:         testDB(t),
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "coder",
		"allow_memory": "yes",
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for non-boolean allow_memory, got success")
	}
}

// TestToolGetAgentSurfacesAllowMemory verifies the read path: agentJSON must
// include allow_memory as a boolean so MCP callers — and downstream UI clients
// using the same wire shape — see the effective value.
func TestToolGetAgentSurfacesAllowMemory(t *testing.T) {
	t.Parallel()
	cfg := fixtureConfig()
	ff := false
	cfg.Agents = []fleet.Agent{
		{Name: "coder", Backend: "claude", Prompt: "p"},
		{Name: "stateless", Backend: "claude", Prompt: "p", AllowMemory: &ff},
	}
	deps := newTestDeps(t, cfg, &stubQueue{}, stubStatus{})
	// toolGetAgent reads from the DB, not from deps.Config; seed the agents
	// the test cares about.
	for _, a := range cfg.Agents {
		if err := store.UpsertAgent(deps.DB, a); err != nil {
			t.Fatalf("seed agent %q: %v", a.Name, err)
		}
	}

	// Default-true agent.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "coder"}
	res, err := toolGetAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("get_agent coder: err=%v body=%s", err, textOf(t, res))
	}
	var out map[string]any
	decodeText(t, res, &out)
	if got, ok := out["allow_memory"].(bool); !ok || !got {
		t.Errorf("coder allow_memory: got %v (%T), want true", out["allow_memory"], out["allow_memory"])
	}

	// Explicit-false agent.
	req2 := mcpgo.CallToolRequest{}
	req2.Params.Arguments = map[string]any{"name": "stateless"}
	res, err = toolGetAgent(deps)(context.Background(), req2)
	if err != nil || res.IsError {
		t.Fatalf("get_agent stateless: err=%v body=%s", err, textOf(t, res))
	}
	decodeText(t, res, &out)
	if got, ok := out["allow_memory"].(bool); !ok || got {
		t.Errorf("stateless allow_memory: got %v (%T), want false", out["allow_memory"], out["allow_memory"])
	}
}
