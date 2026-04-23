package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// stubConfig returns a fixed *config.Config for the tool tests.
type stubConfig struct{ cfg *config.Config }

func (s stubConfig) Config() *config.Config { return s.cfg }

// stubQueue records PushEvent invocations so tests can assert on the event
// shape without running a real workflow engine.
type stubQueue struct {
	mu     sync.Mutex
	events []workflow.Event
	err    error
}

func (q *stubQueue) PushEvent(_ context.Context, ev workflow.Event) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.events = append(q.events, ev)
	return nil
}

func (q *stubQueue) snapshot() []workflow.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]workflow.Event, len(q.events))
	copy(out, q.events)
	return out
}

type stubStatus struct {
	body []byte
	err  error
}

func (s stubStatus) StatusJSON() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.body, nil
}

func fixtureConfig() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			AIBackends: map[string]config.AIBackendConfig{
				"claude": {Command: "claude", Models: []string{"opus", "sonnet"}, Healthy: true, TimeoutSeconds: 60},
				"codex":  {Command: "codex", Healthy: false},
			},
		},
		Skills: map[string]config.SkillDef{
			"testing":  {Prompt: "write good tests"},
			"security": {Prompt: "audit inputs"},
		},
		Agents: []config.AgentDef{
			{Name: "coder", Backend: "claude", Skills: []string{"testing"}, Description: "writes code"},
			{Name: "reviewer", Backend: "claude", AllowDispatch: true},
		},
		Repos: []config.RepoDef{
			{Name: "owner/one", Enabled: true, Use: []config.Binding{
				{Agent: "coder", Labels: []string{"bug"}},
				{Agent: "reviewer", Cron: "@hourly"},
			}},
			{Name: "owner/two", Enabled: false},
		},
	}
}

func newTestDeps(cfg *config.Config, queue EventQueue, status StatusSource) Deps {
	return Deps{
		Config: stubConfig{cfg: cfg},
		Queue:  queue,
		Status: status,
		Logger: zerolog.Nop(),
	}
}

// decodeText unmarshalls the text content of a CallToolResult into v.
func decodeText(t *testing.T, res *mcpgo.CallToolResult, v any) {
	t.Helper()
	if res == nil {
		t.Fatal("expected CallToolResult, got nil")
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("result has no content")
	}
	text, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), v); err != nil {
		t.Fatalf("unmarshal result %q: %v", text.Text, err)
	}
}

func textOf(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	tc, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func TestToolListAgents(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	res, err := toolListAgents(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(got), got)
	}
	if got[0]["name"] != "coder" || got[1]["name"] != "reviewer" {
		t.Fatalf("unexpected agent names: %+v", got)
	}
	// nil-safe slices show up as [] rather than null.
	if skills, ok := got[1]["skills"].([]any); !ok || len(skills) != 0 {
		t.Fatalf("reviewer skills should be []: %+v", got[1]["skills"])
	}
	if got[1]["allow_dispatch"] != true {
		t.Fatalf("reviewer should have allow_dispatch=true")
	}
}

func TestToolListSkillsSorted(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	res, err := toolListSkills(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(got))
	}
	if got[0]["name"] != "security" || got[1]["name"] != "testing" {
		t.Fatalf("skills should be sorted alphabetically, got %+v", got)
	}
}

func TestToolListBackendsSorted(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	res, err := toolListBackends(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 || got[0]["name"] != "claude" || got[1]["name"] != "codex" {
		t.Fatalf("backends should be sorted alphabetically, got %+v", got)
	}
	models, ok := got[0]["models"].([]any)
	if !ok || len(models) != 2 {
		t.Fatalf("claude models should be [opus sonnet], got %+v", got[0]["models"])
	}
}

func TestToolListRepos(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	res, err := toolListRepos(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(got))
	}
	one := got[0]
	if one["name"] != "owner/one" || one["enabled"] != true {
		t.Fatalf("unexpected repo 0: %+v", one)
	}
	bindings, ok := one["bindings"].([]any)
	if !ok || len(bindings) != 2 {
		t.Fatalf("owner/one should have 2 bindings, got %+v", one["bindings"])
	}
	// Label binding carries labels, not cron; cron binding carries cron, not labels.
	labelBinding := bindings[0].(map[string]any)
	cronBinding := bindings[1].(map[string]any)
	if _, hasCron := labelBinding["cron"]; hasCron {
		t.Errorf("label binding should not include cron field: %+v", labelBinding)
	}
	if _, hasLabels := cronBinding["labels"]; hasLabels {
		t.Errorf("cron binding should not include labels field: %+v", cronBinding)
	}
}

func TestToolGetStatusPassesThrough(t *testing.T) {
	t.Parallel()
	want := `{"status":"ok","uptime_seconds":42}`
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{body: []byte(want)})

	res, err := toolGetStatus(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := textOf(t, res); got != want {
		t.Fatalf("status passthrough mismatch: want %q, got %q", want, got)
	}
}

func TestToolGetStatusSurfacesError(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{err: errors.New("boom")})

	res, err := toolGetStatus(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler should return tool-level error, not transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true, got %+v", res)
	}
}

func TestToolTriggerAgentSuccess(t *testing.T) {
	t.Parallel()
	queue := &stubQueue{}
	deps := newTestDeps(fixtureConfig(), queue, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner/one"}

	res, err := toolTriggerAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, res))
	}
	var got map[string]string
	decodeText(t, res, &got)
	if got["status"] != "queued" || got["agent"] != "coder" || got["repo"] != "owner/one" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got["event_id"] == "" {
		t.Fatal("event_id should be populated")
	}
	events := queue.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 queued event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != "agents.run" || ev.Actor != "mcp" || ev.Repo.FullName != "owner/one" {
		t.Fatalf("unexpected event shape: %+v", ev)
	}
	if target, _ := ev.Payload["target_agent"].(string); target != "coder" {
		t.Fatalf("expected target_agent=coder, got %+v", ev.Payload)
	}
}

func TestToolTriggerAgentRejectsUnknownRepo(t *testing.T) {
	t.Parallel()
	queue := &stubQueue{}
	deps := newTestDeps(fixtureConfig(), queue, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner/unknown"}

	res, err := toolTriggerAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown repo, got %+v", res)
	}
	if len(queue.snapshot()) != 0 {
		t.Fatalf("queue should not receive events for invalid repos")
	}
}

func TestToolTriggerAgentRejectsDisabledRepo(t *testing.T) {
	t.Parallel()
	queue := &stubQueue{}
	deps := newTestDeps(fixtureConfig(), queue, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner/two"}

	res, err := toolTriggerAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for disabled repo, got %+v", res)
	}
}

func TestToolTriggerAgentMissingArgs(t *testing.T) {
	t.Parallel()
	queue := &stubQueue{}
	deps := newTestDeps(fixtureConfig(), queue, stubStatus{})

	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing both", map[string]any{}},
		{"missing agent", map[string]any{"repo": "owner/one"}},
		{"missing repo", map[string]any{"agent": "coder"}},
		{"blank agent", map[string]any{"agent": "   ", "repo": "owner/one"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = tc.args
			res, err := toolTriggerAgent(deps)(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError=true for %s, got %+v", tc.name, res)
			}
		})
	}
}

func TestToolTriggerAgentQueueFailure(t *testing.T) {
	t.Parallel()
	queue := &stubQueue{err: errors.New("queue full")}
	deps := newTestDeps(fixtureConfig(), queue, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner/one"}

	res, err := toolTriggerAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for queue failure, got %+v", res)
	}
}
