package mcp

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// TestStringSliceArgAcceptsShapes pins the shape contract for the
// transport-permissive helper introduced for issue #278: native []any,
// native []string, and JSON-encoded array strings all decode the same way,
// while bogus inputs produce a clear "must be an array of strings" error
// that names the offending field.
func TestStringSliceArgAcceptsShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      any
		want    []string
		wantErr string
	}{
		{"nil yields nil with no error", nil, nil, ""},
		{"native []any of strings", []any{"a", "b"}, []string{"a", "b"}, ""},
		{"native []string", []string{"x"}, []string{"x"}, ""},
		{"JSON-encoded array string", `["a","b"]`, []string{"a", "b"}, ""},
		{"JSON-encoded empty array", `[]`, []string{}, ""},
		{"JSON-encoded null decodes as nil slice", `null`, nil, ""},
		{"JSON-encoded array with spaces", `[ "a" , "b" ]`, []string{"a", "b"}, ""},
		{"non-array string is rejected", "ready", nil, "skills must be an array of strings"},
		{"empty string is rejected", "", nil, "skills must be an array of strings"},
		{"wrong scalar type is rejected", 42, nil, "skills must be an array of strings"},
		{"wrong element type points at index", []any{"a", 2}, nil, "skills[1] must be a string"},
		{"JSON-encoded number rejected", `123`, nil, "skills must be an array of strings"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, errMsg := stringSliceArg(tc.in, "skills")
			if errMsg != tc.wantErr {
				t.Fatalf("err = %q, want %q", errMsg, tc.wantErr)
			}
			if tc.wantErr != "" {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestStringSlicePtrArgAcceptsJSONString pins that PATCH-style helpers also
// accept JSON-encoded array strings. The pointer return distinguishes
// "absent" (preserve) from "explicit empty" (clear); both shapes must reach
// the same outcome regardless of whether the client sent a native array or
// a stringified one.
func TestStringSlicePtrArgAcceptsJSONString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    map[string]any
		wantPtr bool
		wantLen int
		wantErr string
	}{
		{"absent → nil ptr, no error", map[string]any{}, false, 0, ""},
		{"null → nil ptr, no error", map[string]any{"skills": nil}, false, 0, ""},
		{"native array → ptr to slice", map[string]any{"skills": []any{"a"}}, true, 1, ""},
		{"JSON-string array → ptr to slice", map[string]any{"skills": `["a","b"]`}, true, 2, ""},
		{"JSON-string empty array → ptr to empty", map[string]any{"skills": `[]`}, true, 0, ""},
		{"JSON-string null → ptr to nil slice (explicit clear)", map[string]any{"skills": `null`}, true, 0, ""},
		{"bad shape → error", map[string]any{"skills": "ready"}, false, 0, "skills must be an array of strings"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ptr, ok, errMsg := stringSlicePtrArg(tc.args, "skills")
			if errMsg != tc.wantErr {
				t.Fatalf("err = %q, want %q", errMsg, tc.wantErr)
			}
			if tc.wantErr != "" {
				return
			}
			if ok != tc.wantPtr {
				t.Fatalf("ok = %v, want %v", ok, tc.wantPtr)
			}
			if !tc.wantPtr {
				if ptr != nil {
					t.Fatalf("ptr = %v, want nil", *ptr)
				}
				return
			}
			if ptr == nil {
				t.Fatal("ptr = nil, want non-nil")
			}
			if got := len(*ptr); got != tc.wantLen {
				t.Fatalf("len = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

// TestArrayOfAnyAcceptsShapes pins the shape contract for the helper that
// decodes nested-object tool arguments (e.g. create_repo's "bindings"
// payload). Mirrors TestStringSliceArgAcceptsShapes: native []any, JSON-
// encoded array strings, and JSON-encoded null all succeed; non-array
// inputs surface a clear "must be an array" error that names the field.
func TestArrayOfAnyAcceptsShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      any
		wantLen int
		wantNil bool
		wantErr string
	}{
		{"native []any passthrough", []any{map[string]any{"agent": "coder"}}, 1, false, ""},
		{"native empty []any", []any{}, 0, false, ""},
		{"JSON-encoded array of objects", `[{"agent":"coder"}]`, 1, false, ""},
		{"JSON-encoded empty array", `[]`, 0, false, ""},
		{"JSON-encoded null decodes as nil slice", `null`, 0, true, ""},
		{"nil falls through to error", nil, 0, true, "bindings must be an array"},
		{"non-array string rejected", "not-an-array", 0, true, "bindings must be an array"},
		{"JSON-encoded number rejected", `123`, 0, true, "bindings must be an array"},
		{"wrong scalar type rejected", 42, 0, true, "bindings must be an array"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, errMsg := arrayOfAny(tc.in, "bindings")
			if errMsg != tc.wantErr {
				t.Fatalf("err = %q, want %q", errMsg, tc.wantErr)
			}
			if tc.wantErr != "" {
				return
			}
			if tc.wantNil && got != nil {
				t.Fatalf("got = %v, want nil", got)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}

// TestToolUpdateAgentAcceptsJSONStringSkills is the regression test for the
// issue's primary repro: an MCP client that delivers `skills` as a
// JSON-encoded string must not be rejected with "skills must be an array of
// strings". The decoded slice must reach the persisted entity intact so the
// patch lands as the caller intended.
func TestToolUpdateAgentAcceptsJSONStringSkills(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "coder",
		"skills":       `["testing","security"]`,
		"can_dispatch": `["reviewer"]`,
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_agent failed: err=%v body=%s", err, textOf(t, res))
	}
	updated, ok := agentByName(t, deps.DB, "coder")
	if !ok {
		t.Fatal("coder missing after update")
	}
	if len(updated.Skills) != 2 || updated.Skills[0] != "testing" || updated.Skills[1] != "security" {
		t.Errorf("skills patch not persisted: %+v", updated.Skills)
	}
	if len(updated.CanDispatch) != 1 || updated.CanDispatch[0] != "reviewer" {
		t.Errorf("can_dispatch patch not persisted: %+v", updated.CanDispatch)
	}
}

// TestToolUpdateBackendAcceptsJSONStringModels mirrors the agent test for
// the backend tool — same transport bug, same fix, different field. Pins
// that the relief is uniform across the CRUD surface.
func TestToolUpdateBackendAcceptsJSONStringModels(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "claude",
		"models": `["opus","sonnet"]`,
	}
	res, err := toolUpdateBackend(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_backend failed: err=%v body=%s", err, textOf(t, res))
	}
	b, ok := backendByName(t, deps.DB, "claude")
	if !ok {
		t.Fatal("claude backend missing after update")
	}
	if len(b.Models) != 2 || b.Models[0] != "opus" || b.Models[1] != "sonnet" {
		t.Errorf("models patch not persisted: %+v", b.Models)
	}
}

// TestToolCreateAgentAcceptsJSONStringSlices pins that POST-style handlers
// also accept JSON-encoded arrays. The acceptance criterion for the issue is
// that the same JSON-string relief applies across CRUD tools.
func TestToolCreateAgentAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "linter",
		"backend":      "claude",
		"prompt":       "audit",
		"skills":       `["security","testing"]`,
		"can_dispatch": `["coder"]`,
	}
	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent failed: err=%v body=%s", err, textOf(t, res))
	}
	persisted, ok := agentByName(t, deps.DB, "linter")
	if !ok {
		t.Fatal("linter missing after create")
	}
	if len(persisted.Skills) != 2 || persisted.Skills[0] != "security" || persisted.Skills[1] != "testing" {
		t.Errorf("skills not persisted from JSON-string: %+v", persisted.Skills)
	}
	if len(persisted.CanDispatch) != 1 || persisted.CanDispatch[0] != "coder" {
		t.Errorf("can_dispatch not persisted from JSON-string: %+v", persisted.CanDispatch)
	}
}

// TestToolCreateBindingAcceptsJSONStringSlices pins that labels/events on
// create_binding also flow through the JSON-string path. The binding tools
// are the highest-traffic surface for parallel MCP calls (the original
// repro), so the relief must reach them too.
func TestToolCreateBindingAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   "owner/one",
		"agent":  "coder",
		"labels": `["ai:fix","ready"]`,
	}
	res, err := toolCreateBinding(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_binding failed: err=%v body=%s", err, textOf(t, res))
	}
	r, _ := repoByName(t, deps.DB, "owner/one")
	found := false
	for _, b := range r.Use {
		if b.Agent == "coder" && len(b.Labels) == 2 && b.Labels[0] == "ai:fix" && b.Labels[1] == "ready" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("labels not persisted from JSON-string: %+v", r.Use)
	}
}

// TestToolUpdateBindingAcceptsJSONStringSlices is the PUT-replace mirror of
// the create_binding test — same fields, same shape contract.
func TestToolUpdateBindingAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id := firstBindingID(t, deps, "owner/one")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"id":     float64(id),
		"repo":   "owner/one",
		"agent":  "coder",
		"labels": `["ready"]`,
	}
	res, err := toolUpdateBinding(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_binding failed: err=%v body=%s", err, textOf(t, res))
	}
	r, _ := repoByName(t, deps.DB, "owner/one")
	for _, b := range r.Use {
		if b.ID != id {
			continue
		}
		if len(b.Labels) != 1 || b.Labels[0] != "ready" {
			t.Errorf("labels not persisted from JSON-string: %+v", b.Labels)
		}
		return
	}
	t.Fatalf("binding %d missing after update", id)
}

// TestToolCreateRepoAcceptsJSONStringBindings pins that the nested
// "bindings" payload also accepts a JSON-encoded array string at the top
// level — the same MCP-transport quirk surfaces here too because the
// argument is itself an array.
func TestToolCreateRepoAcceptsJSONStringBindings(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":     "owner/jsonbindings",
		"enabled":  true,
		"bindings": `[{"agent":"coder","labels":["ready"]}]`,
	}
	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	persisted, ok := repoByName(t, deps.DB, "owner/jsonbindings")
	if !ok {
		t.Fatal("repo missing after create")
	}
	if len(persisted.Use) != 1 {
		t.Fatalf("bindings: want 1, got %d", len(persisted.Use))
	}
	b := persisted.Use[0]
	if b.Agent != "coder" {
		t.Errorf("agent: got %q, want %q", b.Agent, "coder")
	}
	if len(b.Labels) != 1 || b.Labels[0] != "ready" {
		t.Errorf("labels: got %+v, want [ready]", b.Labels)
	}
}

// TestToolUpdateAgentRejectsBogusSkills pins the "still rejects garbage"
// half of the fix: a non-array, non-stringified-array input must surface
// the clear array-of-strings error rather than silently slipping through.
func TestToolUpdateAgentRejectsBogusSkills(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "coder",
		"skills": "not-an-array",
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for bogus skills")
	}
	if got := textOf(t, res); !strings.Contains(got, "skills must be an array of strings") {
		t.Fatalf("error body want %q, got %q", "skills must be an array of strings", got)
	}
}
