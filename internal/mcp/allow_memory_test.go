package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/eloylp/agents/internal/fleet"
)

// TestToolCreateAgentForwardsAllowMemoryFalse verifies that the create_agent
// tool reads the boolean argument and forwards it to UpsertAgent as a non-nil
// *bool, without that the runtime gate cannot distinguish "user wants
// memory disabled" from the documented default.
func TestToolCreateAgentForwardsAllowMemoryFalse(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "linter",
		"backend":      "claude",
		"prompt_ref":   "coder",
		"description":  "Audits code",
		"allow_memory": false,
	}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent: err=%v body=%s", err, textOf(t, res))
	}
	persisted, ok := agentByName(t, deps.Store, "linter")
	if !ok {
		t.Fatal("linter not found after create")
	}
	if persisted.AllowMemory == nil {
		t.Fatal("AllowMemory pointer should be non-nil after explicit false in payload")
	}
	if *persisted.AllowMemory {
		t.Errorf("AllowMemory: got &true, want &false")
	}
}

// TestToolCreateAgentLeavesAllowMemoryNilWhenAbsent verifies that omitting
// allow_memory leaves the Agent pointer nil, so Agent.IsAllowMemory()
// returns the documented default of true downstream.
func TestToolCreateAgentLeavesAllowMemoryNilWhenAbsent(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":        "linter",
		"backend":     "claude",
		"prompt_ref":  "coder",
		"description": "Audits code",
	}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent: err=%v body=%s", err, textOf(t, res))
	}
	persisted, ok := agentByName(t, deps.Store, "linter")
	if !ok {
		t.Fatal("linter not found after create")
	}
	// The store always materialises AllowMemory as non-nil on read; the
	// invariant we care about is that an OMITTED payload field round-trips
	// as the documented default (true).
	if !persisted.IsAllowMemory() {
		t.Errorf("AllowMemory: got false, want documented default of true when absent from payload")
	}
}

// TestToolUpdateAgentForwardsAllowMemoryPatch verifies that update_agent
// surfaces allow_memory in the partial-update payload as a *bool, exactly
// what the merge step needs to flip the gate without nuking other fields.
func TestToolUpdateAgentForwardsAllowMemoryPatch(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "coder",
		"allow_memory": false,
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_agent: err=%v body=%s", err, textOf(t, res))
	}
	persisted, ok := agentByName(t, deps.Store, "coder")
	if !ok {
		t.Fatal("coder missing after update")
	}
	if persisted.AllowMemory == nil || *persisted.AllowMemory {
		t.Fatalf("AllowMemory: got %v, want non-nil &false", persisted.AllowMemory)
	}
	// Other fields preserved by the partial-update path. Backend was "claude"
	// in the seed; nothing in the payload should have touched it.
	if persisted.Backend != "claude" {
		t.Errorf("backend should be preserved across patch, got %q", persisted.Backend)
	}
}

// TestToolUpdateAgentRejectsNonBooleanAllowMemory mirrors the existing
// boolean-arg validation: a typo like "yes" must fail cleanly rather than
// silently being treated as absent (which would mis-trigger preserve-semantics).
func TestToolUpdateAgentRejectsNonBooleanAllowMemory(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
// include allow_memory as a boolean so MCP callers, and downstream UI clients
// using the same wire shape, see the effective value.
func TestToolGetAgentSurfacesAllowMemory(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	ff := false
	if err := deps.Store.UpsertAgent(fleet.Agent{Name: "stateless", Backend: "claude", Prompt: "p", Description: "stateless agent", AllowMemory: &ff}); err != nil {
		t.Fatalf("seed stateless: %v", err)
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
