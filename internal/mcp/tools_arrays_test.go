package mcp

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
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
// strings". The decoded slice must reach the writer with the expected
// elements so the patch lands as the caller intended.
func TestToolUpdateAgentAcceptsJSONStringSkills(t *testing.T) {
	t.Parallel()
	canonical := config.AgentDef{Name: "coder", Backend: "codex", Prompt: "p"}
	w := &stubAgentWriter{patchCanonical: canonical}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		AgentWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "coder",
		"skills":       `["architect","go-testing"]`,
		"can_dispatch": `["pr-reviewer"]`,
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_agent failed: err=%v body=%s", err, textOf(t, res))
	}
	if w.gotPatch.Skills == nil || len(*w.gotPatch.Skills) != 2 {
		t.Fatalf("skills patch not forwarded: %+v", w.gotPatch.Skills)
	}
	if (*w.gotPatch.Skills)[0] != "architect" || (*w.gotPatch.Skills)[1] != "go-testing" {
		t.Errorf("skills contents wrong: %+v", *w.gotPatch.Skills)
	}
	if w.gotPatch.CanDispatch == nil || len(*w.gotPatch.CanDispatch) != 1 ||
		(*w.gotPatch.CanDispatch)[0] != "pr-reviewer" {
		t.Errorf("can_dispatch patch not forwarded: %+v", w.gotPatch.CanDispatch)
	}
}

// TestToolUpdateBackendAcceptsJSONStringModels mirrors the agent test for
// the backend tool — same transport bug, same fix, different field. Pins
// that the relief is uniform across the CRUD surface.
func TestToolUpdateBackendAcceptsJSONStringModels(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{
		patchCanonicalName:   "claude",
		patchCanonicalConfig: config.AIBackendConfig{Command: "/bin/claude"},
	}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		BackendWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "claude",
		"models": `["opus","sonnet"]`,
	}
	res, err := toolUpdateBackend(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_backend failed: err=%v body=%s", err, textOf(t, res))
	}
	if w.gotPatch.Models == nil || len(*w.gotPatch.Models) != 2 {
		t.Fatalf("models patch not forwarded: %+v", w.gotPatch.Models)
	}
	if (*w.gotPatch.Models)[0] != "opus" || (*w.gotPatch.Models)[1] != "sonnet" {
		t.Errorf("models contents wrong: %+v", *w.gotPatch.Models)
	}
}

// TestToolCreateAgentAcceptsJSONStringSlices pins that POST-style handlers
// also accept JSON-encoded arrays. Today these go through req.GetStringSlice
// which silently drops bad shapes; the issue's acceptance criterion is that
// the same JSON-string relief applies across CRUD tools.
func TestToolCreateAgentAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	canonical := config.AgentDef{Name: "linter", Backend: "claude", Skills: []string{"security"}}
	w := &stubAgentWriter{canonical: canonical}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		AgentWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":         "linter",
		"backend":      "claude",
		"skills":       `["security","compliance"]`,
		"can_dispatch": `["coder"]`,
	}
	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_agent failed: err=%v body=%s", err, textOf(t, res))
	}
	if len(w.gotUpsert.Skills) != 2 ||
		w.gotUpsert.Skills[0] != "security" || w.gotUpsert.Skills[1] != "compliance" {
		t.Errorf("skills not forwarded from JSON-string: %+v", w.gotUpsert.Skills)
	}
	if len(w.gotUpsert.CanDispatch) != 1 || w.gotUpsert.CanDispatch[0] != "coder" {
		t.Errorf("can_dispatch not forwarded from JSON-string: %+v", w.gotUpsert.CanDispatch)
	}
}

// TestToolCreateBindingAcceptsJSONStringSlices pins that labels/events on
// create_binding also flow through the JSON-string path. The binding tools
// are the highest-traffic surface for parallel MCP calls (the original
// repro), so the relief must reach them too.
func TestToolCreateBindingAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	w := &stubBindingWriter{
		createResult: config.Binding{ID: 1, Agent: "coder"},
	}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		BindingWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   "owner/repo",
		"agent":  "coder",
		"labels": `["ai:fix","ready"]`,
		"events": `["push","pull_request"]`,
	}
	res, err := toolCreateBinding(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("create_binding failed: err=%v body=%s", err, textOf(t, res))
	}
	if got := w.gotCreateBinding.Labels; len(got) != 2 || got[0] != "ai:fix" || got[1] != "ready" {
		t.Errorf("labels not forwarded from JSON-string: %+v", got)
	}
	if got := w.gotCreateBinding.Events; len(got) != 2 || got[0] != "push" || got[1] != "pull_request" {
		t.Errorf("events not forwarded from JSON-string: %+v", got)
	}
}

// TestToolUpdateBindingAcceptsJSONStringSlices is the PUT-replace mirror of
// the create_binding test — same fields, same shape contract.
func TestToolUpdateBindingAcceptsJSONStringSlices(t *testing.T) {
	t.Parallel()
	w := &stubBindingWriter{
		updateResult: config.Binding{ID: 5, Agent: "coder"},
	}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		BindingWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"id":     float64(5),
		"repo":   "owner/repo",
		"agent":  "coder",
		"labels": `["ready"]`,
		"events": `["push"]`,
	}
	res, err := toolUpdateBinding(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_binding failed: err=%v body=%s", err, textOf(t, res))
	}
	if got := w.gotUpdateBinding.Labels; len(got) != 1 || got[0] != "ready" {
		t.Errorf("labels not forwarded from JSON-string: %+v", got)
	}
	if got := w.gotUpdateBinding.Events; len(got) != 1 || got[0] != "push" {
		t.Errorf("events not forwarded from JSON-string: %+v", got)
	}
}

// TestToolCreateRepoAcceptsJSONStringBindings pins that the nested
// "bindings" payload also accepts a JSON-encoded array string at the top
// level — the same MCP-transport quirk surfaces here too because the
// argument is itself an array.
func TestToolCreateRepoAcceptsJSONStringBindings(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{canonical: config.RepoDef{Name: "owner/repo", Enabled: true}}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		RepoWrite: w, Logger: zerolog.Nop(),
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":     "owner/repo",
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
	if len(w.gotUpsert.Use) != 1 {
		t.Fatalf("bindings: want 1, got %d", len(w.gotUpsert.Use))
	}
	b := w.gotUpsert.Use[0]
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
	w := &stubAgentWriter{}
	deps := Deps{
		DB: testDB(t), Config: stubConfig{cfg: fixtureConfig()},
		Queue: &stubQueue{}, Status: stubStatus{},
		AgentWrite: w, Logger: zerolog.Nop(),
	}
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
	if w.gotPatchName != "" {
		t.Errorf("writer must not be invoked on validation failure, got name=%q", w.gotPatchName)
	}
}
