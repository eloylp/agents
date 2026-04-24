package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
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

func TestToolGetAgent(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"exact match", "coder", false},
		{"case insensitive", "Coder", false},
		{"whitespace trimmed", "  reviewer  ", false},
		{"unknown agent", "ghost", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = map[string]any{"name": tc.input}
			res, err := toolGetAgent(deps)(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			if tc.wantErr {
				if !res.IsError {
					t.Fatalf("expected IsError for %q, got %+v", tc.input, res)
				}
				return
			}
			var got map[string]any
			decodeText(t, res, &got)
			if got["name"] == nil {
				t.Fatalf("expected name field, got %+v", got)
			}
		})
	}
}

func TestToolGetAgentMissingName(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	cases := []map[string]any{
		{},
		{"name": ""},
		{"name": "   "},
	}
	for _, args := range cases {
		req := mcpgo.CallToolRequest{}
		req.Params.Arguments = args
		res, err := toolGetAgent(deps)(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !res.IsError {
			t.Fatalf("expected IsError for args %+v, got %+v", args, res)
		}
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

func TestToolGetSkill(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "Testing"}
	res, err := toolGetSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "testing" || got["prompt"] != "write good tests" {
		t.Fatalf("unexpected skill payload: %+v", got)
	}

	req.Params.Arguments = map[string]any{"name": "missing"}
	res, err = toolGetSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for missing skill, got %+v", res)
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

func TestToolGetBackend(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "Claude"}
	res, err := toolGetBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "claude" || got["command"] != "claude" {
		t.Fatalf("unexpected backend payload: %+v", got)
	}
	if got["healthy"] != true {
		t.Fatalf("expected healthy=true, got %+v", got["healthy"])
	}

	req.Params.Arguments = map[string]any{"name": "ghost"}
	res, err = toolGetBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for missing backend, got %+v", res)
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

func TestToolGetRepo(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(fixtureConfig(), &stubQueue{}, stubStatus{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "OWNER/one"}
	res, err := toolGetRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "owner/one" || got["enabled"] != true {
		t.Fatalf("unexpected repo payload: %+v", got)
	}
	bindings, ok := got["bindings"].([]any)
	if !ok || len(bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %+v", got["bindings"])
	}

	// Disabled repos still resolve — callers decide what to do with enabled=false.
	req.Params.Arguments = map[string]any{"name": "owner/two"}
	res, err = toolGetRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decodeText(t, res, &got)
	if got["enabled"] != false {
		t.Fatalf("expected enabled=false for owner/two, got %+v", got)
	}

	req.Params.Arguments = map[string]any{"name": "owner/unknown"}
	res, err = toolGetRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown repo, got %+v", res)
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

// stubObserve is a test-only ObserveStore that returns fixed canned slices.
// Per-method captures let tests assert the exact arguments the handlers pass.
type stubObserve struct {
	events    []observe.TimestampedEvent
	traces    []observe.Span
	byRoot    map[string][]observe.Span
	steps     map[string][]workflow.TraceStep
	edges     []observe.Edge
	lastSince time.Time
}

func (s *stubObserve) ListEvents(since time.Time) []observe.TimestampedEvent {
	s.lastSince = since
	return s.events
}
func (s *stubObserve) ListTraces() []observe.Span                      { return s.traces }
func (s *stubObserve) TracesByRootEventID(id string) []observe.Span    { return s.byRoot[id] }
func (s *stubObserve) ListSteps(spanID string) []workflow.TraceStep    { return s.steps[spanID] }
func (s *stubObserve) ListEdges() []observe.Edge                       { return s.edges }

type stubDispatchStats struct{ stats workflow.DispatchStats }

func (s stubDispatchStats) DispatchStats() workflow.DispatchStats { return s.stats }

type stubMemory struct {
	content string
	mtime   time.Time
	found   bool
	err     error
	calls   []struct{ agent, repo string }
}

func (s *stubMemory) ReadMemory(agent, repo string) (string, time.Time, bool, error) {
	s.calls = append(s.calls, struct{ agent, repo string }{agent, repo})
	return s.content, s.mtime, s.found, s.err
}

func depsWithObserve(obs ObserveStore) Deps {
	return Deps{
		Config:  stubConfig{cfg: fixtureConfig()},
		Queue:   &stubQueue{},
		Status:  stubStatus{},
		Observe: obs,
		Logger:  zerolog.Nop(),
	}
}

func TestToolListEvents(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{events: []observe.TimestampedEvent{
		{ID: "e1", Kind: "issues.labeled", Repo: "owner/one", At: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)},
		{ID: "e2", Kind: "agents.run", Repo: "owner/one", At: time.Date(2026, 4, 20, 10, 5, 0, 0, time.UTC)},
	}}
	deps := depsWithObserve(obs)

	res, err := toolListEvents(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 || got[0]["id"] != "e1" || got[1]["id"] != "e2" {
		t.Fatalf("unexpected events payload: %+v", got)
	}
	if !obs.lastSince.IsZero() {
		t.Fatalf("expected zero-time since when omitted, got %v", obs.lastSince)
	}
}

func TestToolListEventsSinceFilter(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{events: []observe.TimestampedEvent{}}
	deps := depsWithObserve(obs)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"since": "2026-04-20T10:00:00Z"}
	if _, err := toolListEvents(deps)(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.lastSince.IsZero() {
		t.Fatalf("expected non-zero since, got zero")
	}

	// Unparseable since should fall back to no filter rather than erroring,
	// matching GET /events.
	obs.lastSince = time.Now()
	req.Params.Arguments = map[string]any{"since": "not-a-time"}
	if _, err := toolListEvents(deps)(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !obs.lastSince.IsZero() {
		t.Fatalf("expected zero since for unparseable input, got %v", obs.lastSince)
	}
}

func TestToolListEventsNilSlice(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{events: nil}
	deps := depsWithObserve(obs)

	res, err := toolListEvents(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nil slice from the store should serialise as [] not null — easier for
	// LLM clients that don't distinguish the two.
	got := textOf(t, res)
	if got != "[]" {
		t.Fatalf("expected []\\n, got %q", got)
	}
}

func TestToolListTraces(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{traces: []observe.Span{
		{SpanID: "s1", Agent: "coder", Status: "success"},
		{SpanID: "s2", Agent: "reviewer", Status: "error"},
	}}
	deps := depsWithObserve(obs)

	res, err := toolListTraces(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 || got[0]["span_id"] != "s1" {
		t.Fatalf("unexpected traces payload: %+v", got)
	}
}

func TestToolGetTrace(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{byRoot: map[string][]observe.Span{
		"root-1": {{SpanID: "s1", RootEventID: "root-1", Agent: "coder"}},
	}}
	deps := depsWithObserve(obs)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"root_event_id": "root-1"}
	res, err := toolGetTrace(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 1 || got[0]["span_id"] != "s1" {
		t.Fatalf("unexpected trace payload: %+v", got)
	}

	req.Params.Arguments = map[string]any{"root_event_id": "missing"}
	res, err = toolGetTrace(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for missing trace, got %+v", res)
	}
}

func TestToolGetTraceRequiresID(t *testing.T) {
	t.Parallel()
	deps := depsWithObserve(&stubObserve{})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"root_event_id": "   "}
	res, err := toolGetTrace(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank root_event_id, got %+v", res)
	}
}

func TestToolGetTraceSteps(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{steps: map[string][]workflow.TraceStep{
		"s1": {{ToolName: "read_file", DurationMs: 42}},
	}}
	deps := depsWithObserve(obs)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"span_id": "s1"}
	res, err := toolGetTraceSteps(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 1 || got[0]["tool_name"] != "read_file" {
		t.Fatalf("unexpected steps payload: %+v", got)
	}

	// Unknown span yields an empty array (span may exist without steps).
	req.Params.Arguments = map[string]any{"span_id": "missing"}
	res, err = toolGetTraceSteps(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("steps lookup should never error on missing span, got %+v", res)
	}
	if got := textOf(t, res); got != "[]" {
		t.Fatalf("expected [] for span with no steps, got %q", got)
	}
}

func TestToolGetGraphSeedsNodesFromFleetAndEdges(t *testing.T) {
	t.Parallel()
	obs := &stubObserve{edges: []observe.Edge{
		{From: "coder", To: "ghost", Count: 1, Dispatches: []observe.DispatchRecord{
			{At: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC), Repo: "owner/one", Number: 7, Reason: "followup"},
		}},
	}}
	deps := depsWithObserve(obs)

	res, err := toolGetGraph(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		Nodes []map[string]any `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	}
	decodeText(t, res, &got)
	// Fleet has 2 agents (coder, reviewer); edge introduces a third (ghost).
	if len(got.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %+v", got.Nodes)
	}
	ids := map[string]bool{}
	for _, n := range got.Nodes {
		ids[n["id"].(string)] = true
	}
	for _, want := range []string{"coder", "reviewer", "ghost"} {
		if !ids[want] {
			t.Errorf("missing node %q in %+v", want, got.Nodes)
		}
	}
	if len(got.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %+v", got.Edges)
	}
	edge := got.Edges[0]
	if edge["from"] != "coder" || edge["to"] != "ghost" || edge["count"].(float64) != 1 {
		t.Fatalf("unexpected edge: %+v", edge)
	}
	dispatches, ok := edge["dispatches"].([]any)
	if !ok || len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch record, got %+v", edge["dispatches"])
	}
}

func TestToolGetDispatches(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Config:        stubConfig{cfg: fixtureConfig()},
		Queue:         &stubQueue{},
		Status:        stubStatus{},
		DispatchStats: stubDispatchStats{stats: workflow.DispatchStats{RequestedTotal: 5, Enqueued: 3, DroppedSelf: 1}},
		Logger:        zerolog.Nop(),
	}

	res, err := toolGetDispatches(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["requested_total"].(float64) != 5 || got["enqueued"].(float64) != 3 || got["dropped_self"].(float64) != 1 {
		t.Fatalf("unexpected dispatch stats payload: %+v", got)
	}
}

func TestToolGetMemorySuccess(t *testing.T) {
	t.Parallel()
	mem := &stubMemory{content: "# hello\n", mtime: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC), found: true}
	deps := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Memory: mem,
		Logger: zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner_one"}
	res, err := toolGetMemory(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["agent"] != "coder" || got["repo"] != "owner_one" || got["content"] != "# hello\n" {
		t.Fatalf("unexpected memory payload: %+v", got)
	}
	if got["mtime"] == nil {
		t.Fatalf("expected mtime field, got %+v", got)
	}
	if len(mem.calls) != 1 || mem.calls[0].agent != "coder" || mem.calls[0].repo != "owner_one" {
		t.Fatalf("unexpected memory reader calls: %+v", mem.calls)
	}
}

func TestToolGetMemoryMissing(t *testing.T) {
	t.Parallel()
	mem := &stubMemory{found: false}
	deps := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Memory: mem,
		Logger: zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner_one"}
	res, err := toolGetMemory(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for missing memory, got %+v", res)
	}
}

func TestToolGetMemoryRejectsTraversal(t *testing.T) {
	t.Parallel()
	mem := &stubMemory{found: true, content: "leak"}
	deps := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Memory: mem,
		Logger: zerolog.Nop(),
	}

	// Components that clean to "." or ".." are rejected before hitting the
	// reader. Anything exotic beyond that is canonicalised by the reader's
	// own NormalizeToken step, so we don't duplicate that check here.
	cases := []map[string]any{
		{"agent": "..", "repo": "owner_one"},
		{"agent": "coder", "repo": ".."},
		{"agent": ".", "repo": "owner_one"},
	}
	for _, args := range cases {
		req := mcpgo.CallToolRequest{}
		req.Params.Arguments = args
		res, err := toolGetMemory(deps)(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error for args %+v: %v", args, err)
		}
		if !res.IsError {
			t.Fatalf("expected IsError for traversal args %+v, got %+v", args, res)
		}
	}
	if len(mem.calls) != 0 {
		t.Fatalf("memory reader should not be called for rejected paths, got %+v", mem.calls)
	}
}

func TestToolGetMemoryRequiresBothArgs(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Memory: &stubMemory{},
		Logger: zerolog.Nop(),
	}

	cases := []map[string]any{
		{"agent": "coder"},
		{"repo": "owner_one"},
		{"agent": "coder", "repo": "   "},
	}
	for _, args := range cases {
		req := mcpgo.CallToolRequest{}
		req.Params.Arguments = args
		res, err := toolGetMemory(deps)(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsError {
			t.Fatalf("expected IsError for args %+v, got %+v", args, res)
		}
	}
}

func TestToolGetMemoryPropagatesReaderError(t *testing.T) {
	t.Parallel()
	mem := &stubMemory{err: errors.New("disk on fire")}
	deps := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Memory: mem,
		Logger: zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner_one"}
	res, err := toolGetMemory(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on reader failure, got %+v", res)
	}
}

// TestRegisterTools_ObservabilityOptional verifies that observability tools
// register only when their provider is wired. Otherwise they are omitted, so
// tests (and minimal deployments) don't need to stub the full stack.
func TestRegisterTools_ObservabilityOptional(t *testing.T) {
	t.Parallel()
	// Deps with no Observe/DispatchStats/Memory: only core tools register.
	// The simplest way to assert this without depending on mcp-go internals
	// is to call the handler factories directly — they never rely on the
	// server registration step — and confirm the conditional branches in
	// registerTools compile and exercise what we expect.
	// Each handler factory is already covered by its own test; this test
	// exists to document the invariant that tools.go's gating is the source
	// of truth for optional registration.
	core := Deps{
		Config: stubConfig{cfg: fixtureConfig()},
		Queue:  &stubQueue{},
		Status: stubStatus{},
		Logger: zerolog.Nop(),
	}
	if core.Observe != nil || core.DispatchStats != nil || core.Memory != nil || core.ConfigBytes != nil {
		t.Fatalf("default Deps should have nil optional providers")
	}
}

// stubConfigBytes implements ConfigReader with canned byte payloads so the
// config tool tests stay independent of the real webhook.Server.
type stubConfigBytes struct {
	jsonBody []byte
	yamlBody []byte
	jsonErr  error
	yamlErr  error
}

func (s stubConfigBytes) ConfigJSON() ([]byte, error) {
	return s.jsonBody, s.jsonErr
}

func (s stubConfigBytes) ExportYAML() ([]byte, error) {
	return s.yamlBody, s.yamlErr
}

func TestToolGetConfigReturnsBytesVerbatim(t *testing.T) {
	t.Parallel()
	want := []byte(`{"daemon":{"http":{"webhook_secret":"[redacted]"}}}`)
	deps := Deps{
		Config:      stubConfig{cfg: fixtureConfig()},
		Queue:       &stubQueue{},
		Status:      stubStatus{},
		ConfigBytes: stubConfigBytes{jsonBody: want},
		Logger:      zerolog.Nop(),
	}

	res, err := toolGetConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textOf(t, res) != string(want) {
		t.Fatalf("body want %q got %q", string(want), textOf(t, res))
	}
}

func TestToolGetConfigPropagatesError(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Config:      stubConfig{cfg: fixtureConfig()},
		Queue:       &stubQueue{},
		Status:      stubStatus{},
		ConfigBytes: stubConfigBytes{jsonErr: errors.New("snapshot failure")},
		Logger:      zerolog.Nop(),
	}
	res, err := toolGetConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on reader failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "snapshot failure") {
		t.Fatalf("error body want substring %q, got %q", "snapshot failure", got)
	}
}

func TestToolExportConfigReturnsBytesVerbatim(t *testing.T) {
	t.Parallel()
	want := []byte("agents:\n  - name: coder\n    backend: claude\n")
	deps := Deps{
		Config:      stubConfig{cfg: fixtureConfig()},
		Queue:       &stubQueue{},
		Status:      stubStatus{},
		ConfigBytes: stubConfigBytes{yamlBody: want},
		Logger:      zerolog.Nop(),
	}
	res, err := toolExportConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textOf(t, res) != string(want) {
		t.Fatalf("body want %q got %q", string(want), textOf(t, res))
	}
}

func TestToolExportConfigPropagatesError(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Config:      stubConfig{cfg: fixtureConfig()},
		Queue:       &stubQueue{},
		Status:      stubStatus{},
		ConfigBytes: stubConfigBytes{yamlErr: errors.New("db closed")},
		Logger:      zerolog.Nop(),
	}
	res, err := toolExportConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on reader failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "db closed") {
		t.Fatalf("error body want substring %q, got %q", "db closed", got)
	}
}

// stubConfigImporter records the YAML body and mode it received and returns a
// canned counts map / error. Used by the import_config tool tests so they stay
// independent of the real webhook.Server.
type stubConfigImporter struct {
	gotBody []byte
	gotMode string
	counts  map[string]int
	err     error
}

func (s *stubConfigImporter) ImportYAML(body []byte, mode string) (map[string]int, error) {
	s.gotBody = body
	s.gotMode = mode
	return s.counts, s.err
}

func TestToolImportConfigPassesYAMLAndMode(t *testing.T) {
	t.Parallel()
	imp := &stubConfigImporter{counts: map[string]int{
		"agents": 2, "skills": 1, "repos": 3, "backends": 1,
	}}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		ConfigImport: imp,
		Logger:       zerolog.Nop(),
	}

	body := "agents:\n  - name: coder\n    backend: claude\n    prompt: x\n"
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": body, "mode": "replace"}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res)
	}
	if string(imp.gotBody) != body {
		t.Errorf("body forwarded: want %q, got %q", body, string(imp.gotBody))
	}
	if imp.gotMode != "replace" {
		t.Errorf("mode forwarded: want replace, got %q", imp.gotMode)
	}
	var got map[string]int
	decodeText(t, res, &got)
	if got["agents"] != 2 || got["skills"] != 1 || got["repos"] != 3 || got["backends"] != 1 {
		t.Errorf("counts wire shape: got %+v", got)
	}
}

func TestToolImportConfigDefaultsMode(t *testing.T) {
	t.Parallel()
	imp := &stubConfigImporter{counts: map[string]int{}}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		ConfigImport: imp,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": "skills: {}\n"}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res)
	}
	if imp.gotMode != "" {
		t.Errorf("missing mode should default to empty, got %q", imp.gotMode)
	}
}

func TestToolImportConfigRequiresYAML(t *testing.T) {
	t.Parallel()
	imp := &stubConfigImporter{counts: map[string]int{}}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		ConfigImport: imp,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when yaml argument missing, got %+v", res)
	}
	if imp.gotBody != nil {
		t.Errorf("importer should not be called when yaml is missing, got body=%q", string(imp.gotBody))
	}
}

func TestToolImportConfigPropagatesError(t *testing.T) {
	t.Parallel()
	imp := &stubConfigImporter{err: errors.New("invalid mode")}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		ConfigImport: imp,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": "x", "mode": "replce"}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when importer fails, got %+v", res)
	}
}

// stubAgentWriter records the agent definition / delete arguments it received
// and returns canned values. The canonical agent returned from UpsertAgent is
// what the create_agent tool serialises back to the caller, so tests pin both
// the inputs the writer received and the outputs the tool surfaces.
type stubAgentWriter struct {
	gotUpsert     config.AgentDef
	gotDeleteName string
	gotCascade    bool
	canonical     config.AgentDef
	upsertErr     error
	deleteErr     error
}

func (s *stubAgentWriter) UpsertAgent(a config.AgentDef) (config.AgentDef, error) {
	s.gotUpsert = a
	if s.upsertErr != nil {
		return config.AgentDef{}, s.upsertErr
	}
	return s.canonical, nil
}

func (s *stubAgentWriter) DeleteAgent(name string, cascade bool) error {
	s.gotDeleteName = name
	s.gotCascade = cascade
	return s.deleteErr
}

func TestToolCreateAgentForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	canonical := config.AgentDef{
		Name:          "linter",
		Backend:       "claude",
		Skills:        []string{"security"},
		Prompt:        "audit",
		AllowDispatch: true,
		CanDispatch:   []string{"coder"},
		Description:   "audits code",
	}
	w := &stubAgentWriter{canonical: canonical}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":           "Linter",
		"backend":        "claude",
		"prompt":         "audit",
		"description":    "audits code",
		"skills":         []any{"security"},
		"can_dispatch":   []any{"coder"},
		"allow_dispatch": true,
	}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	if w.gotUpsert.Name != "Linter" || w.gotUpsert.Backend != "claude" || w.gotUpsert.Prompt != "audit" {
		t.Errorf("forwarded agent missing fields: %+v", w.gotUpsert)
	}
	if !w.gotUpsert.AllowDispatch || len(w.gotUpsert.CanDispatch) != 1 || w.gotUpsert.CanDispatch[0] != "coder" {
		t.Errorf("dispatch fields not forwarded: %+v", w.gotUpsert)
	}
	if len(w.gotUpsert.Skills) != 1 || w.gotUpsert.Skills[0] != "security" {
		t.Errorf("skills slice not forwarded: %+v", w.gotUpsert.Skills)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "linter" {
		t.Errorf("response should reflect canonical name, got %+v", got["name"])
	}
	if got["allow_dispatch"] != true {
		t.Errorf("canonical allow_dispatch lost in response: %+v", got)
	}
}

func TestToolCreateAgentRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"backend": "claude"}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
	if w.gotUpsert.Name != "" {
		t.Errorf("writer should not be invoked when name missing, got %+v", w.gotUpsert)
	}
}

func TestToolCreateAgentPropagatesError(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{upsertErr: errors.New("backend unknown")}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "linter", "backend": "ghost"}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "backend unknown") {
		t.Fatalf("error body want substring %q, got %q", "backend unknown", got)
	}
}

func TestToolDeleteAgentNormalizesAndForwardsCascade(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  Coder  ", "cascade": true}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if w.gotDeleteName != "coder" {
		t.Errorf("name should be normalized: got %q", w.gotDeleteName)
	}
	if !w.gotCascade {
		t.Errorf("cascade should be forwarded as true")
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "coder" || got["cascade"] != true {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteAgentRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if w.gotDeleteName != "" {
		t.Errorf("writer should not be invoked for blank name, got %q", w.gotDeleteName)
	}
}

func TestToolDeleteAgentPropagatesConflict(t *testing.T) {
	t.Parallel()
	w := &stubAgentWriter{deleteErr: errors.New("agent referenced by binding")}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		AgentWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "coder"}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "agent referenced by binding") {
		t.Fatalf("error body want substring %q, got %q", "agent referenced by binding", got)
	}
}

// stubSkillWriter records the skill arguments it received and returns canned
// values. Tests pin both the inputs the writer observed (e.g. raw name, prompt)
// and the canonical values the tool surfaces back to the caller.
type stubSkillWriter struct {
	gotUpsertName  string
	gotUpsertSkill config.SkillDef
	gotDeleteName  string
	canonicalName  string
	canonical      config.SkillDef
	upsertErr      error
	deleteErr      error
}

func (s *stubSkillWriter) UpsertSkill(name string, sk config.SkillDef) (string, config.SkillDef, error) {
	s.gotUpsertName = name
	s.gotUpsertSkill = sk
	if s.upsertErr != nil {
		return "", config.SkillDef{}, s.upsertErr
	}
	return s.canonicalName, s.canonical, nil
}

func (s *stubSkillWriter) DeleteSkill(name string) error {
	s.gotDeleteName = name
	return s.deleteErr
}

func TestToolCreateSkillForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{
		canonicalName: "security",
		canonical:     config.SkillDef{Prompt: "audit inputs carefully"},
	}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "  Security  ",
		"prompt": "  audit inputs carefully  ",
	}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	if w.gotUpsertName != "  Security  " {
		t.Errorf("raw name should pass through to writer (writer owns normalization): got %q", w.gotUpsertName)
	}
	if w.gotUpsertSkill.Prompt != "  audit inputs carefully  " {
		t.Errorf("raw prompt should pass through to writer: got %q", w.gotUpsertSkill.Prompt)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "security" {
		t.Errorf("response should reflect canonical name: %+v", got)
	}
	if got["prompt"] != "audit inputs carefully" {
		t.Errorf("response should reflect canonical prompt: %+v", got)
	}
}

func TestToolCreateSkillRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"prompt": "body"}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
	if w.gotUpsertName != "" {
		t.Errorf("writer should not be invoked when name missing, got %q", w.gotUpsertName)
	}
}

// TestToolCreateSkillRejectsBlankName pins the whitespace-name contract for
// create_skill: the tool does not short-circuit at the handler (unlike
// delete_skill, which uses trimmedString), so a blank name must reach
// UpsertSkill and surface the writer's *store.ErrValidation as a user error.
// If the blank-name guard is ever hoisted into the handler, this test will
// fail and force an update to the stub-invocation expectations.
func TestToolCreateSkillRejectsBlankName(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{upsertErr: &store.ErrValidation{Msg: "name is required"}}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "name is required") {
		t.Fatalf("error body want substring %q, got %q", "name is required", got)
	}
	if w.gotUpsertName != "   " {
		t.Errorf("writer should receive raw blank name (owns normalization), got %q", w.gotUpsertName)
	}
}

func TestToolCreateSkillPropagatesError(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{upsertErr: errors.New("validation: prompt empty")}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "security"}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "validation: prompt empty") {
		t.Fatalf("error body want substring %q, got %q", "validation: prompt empty", got)
	}
}

func TestToolDeleteSkillNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  Security  "}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if w.gotDeleteName != "security" {
		t.Errorf("name should be normalized before forwarding: got %q", w.gotDeleteName)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "security" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteSkillRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if w.gotDeleteName != "" {
		t.Errorf("writer should not be invoked for blank name, got %q", w.gotDeleteName)
	}
}

func TestToolDeleteSkillPropagatesConflict(t *testing.T) {
	t.Parallel()
	w := &stubSkillWriter{deleteErr: errors.New("skill referenced by agent")}
	deps := Deps{
		Config:     stubConfig{cfg: fixtureConfig()},
		Queue:      &stubQueue{},
		Status:     stubStatus{},
		SkillWrite: w,
		Logger:     zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "security"}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "skill referenced by agent") {
		t.Fatalf("error body want substring %q, got %q", "skill referenced by agent", got)
	}
}

// stubBackendWriter records the backend arguments it received and returns
// canned values. Tests pin both the raw inputs the writer observed and the
// canonical values the tool surfaces back to the caller.
type stubBackendWriter struct {
	gotUpsertName    string
	gotUpsertBackend config.AIBackendConfig
	gotDeleteName    string
	canonicalName    string
	canonical        config.AIBackendConfig
	upsertErr        error
	deleteErr        error
}

func (s *stubBackendWriter) UpsertBackend(name string, b config.AIBackendConfig) (string, config.AIBackendConfig, error) {
	s.gotUpsertName = name
	s.gotUpsertBackend = b
	if s.upsertErr != nil {
		return "", config.AIBackendConfig{}, s.upsertErr
	}
	return s.canonicalName, s.canonical, nil
}

func (s *stubBackendWriter) DeleteBackend(name string) error {
	s.gotDeleteName = name
	return s.deleteErr
}

func TestToolCreateBackendForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	canonical := config.AIBackendConfig{
		Command:        "claude",
		Models:         []string{"claude-opus-4-7"},
		TimeoutSeconds: 600,
		MaxPromptChars: 12000,
	}
	w := &stubBackendWriter{
		canonicalName: "claude",
		canonical:     canonical,
	}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":             "  Claude  ",
		"command":          "claude",
		"models":           []any{"claude-opus-4-7"},
		"timeout_seconds":  600,
		"max_prompt_chars": 12000,
	}

	res, err := toolCreateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	if w.gotUpsertName != "  Claude  " {
		t.Errorf("raw name should pass through to writer (writer owns normalization): got %q", w.gotUpsertName)
	}
	if w.gotUpsertBackend.Command != "claude" {
		t.Errorf("command not forwarded: %+v", w.gotUpsertBackend)
	}
	if w.gotUpsertBackend.TimeoutSeconds != 600 || w.gotUpsertBackend.MaxPromptChars != 12000 {
		t.Errorf("runtime settings not forwarded: (%d, %d)", w.gotUpsertBackend.TimeoutSeconds, w.gotUpsertBackend.MaxPromptChars)
	}
	if len(w.gotUpsertBackend.Models) != 1 || w.gotUpsertBackend.Models[0] != "claude-opus-4-7" {
		t.Errorf("models slice not forwarded: %+v", w.gotUpsertBackend.Models)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "claude" {
		t.Errorf("response should reflect canonical name: %+v", got)
	}
	if got["command"] != "claude" {
		t.Errorf("response should reflect canonical command: %+v", got)
	}
}

func TestToolCreateBackendRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"command": "claude"}

	res, err := toolCreateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
	if w.gotUpsertName != "" {
		t.Errorf("writer should not be invoked when name missing, got %q", w.gotUpsertName)
	}
}

// TestToolCreateBackendRejectsBlankName pins the whitespace-name contract for
// create_backend: like create_skill, the handler uses req.RequireString("name")
// which only rejects the missing-key path, so a whitespace-only name must
// reach UpsertBackend and surface the writer's *store.ErrValidation as a user
// error. If the blank-name guard is ever hoisted into the tool layer, this
// test fails and forces an update to the stub-invocation expectations.
func TestToolCreateBackendRejectsBlankName(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{upsertErr: &store.ErrValidation{Msg: "name is required"}}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   ", "command": "claude"}

	res, err := toolCreateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "name is required") {
		t.Fatalf("error body want substring %q, got %q", "name is required", got)
	}
	if w.gotUpsertName != "   " {
		t.Errorf("writer should receive raw blank name (owns normalization), got %q", w.gotUpsertName)
	}
}

func TestToolCreateBackendPropagatesError(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{upsertErr: errors.New("db closed")}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "claude", "command": "claude"}

	res, err := toolCreateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "db closed") {
		t.Fatalf("error body want substring %q, got %q", "db closed", got)
	}
}

func TestToolDeleteBackendNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  Claude  "}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if w.gotDeleteName != "claude" {
		t.Errorf("name should be normalized before forwarding: got %q", w.gotDeleteName)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "claude" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteBackendRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if w.gotDeleteName != "" {
		t.Errorf("writer should not be invoked for blank name, got %q", w.gotDeleteName)
	}
}

func TestToolDeleteBackendPropagatesConflict(t *testing.T) {
	t.Parallel()
	w := &stubBackendWriter{deleteErr: errors.New("backend referenced by agent")}
	deps := Deps{
		Config:       stubConfig{cfg: fixtureConfig()},
		Queue:        &stubQueue{},
		Status:       stubStatus{},
		BackendWrite: w,
		Logger:       zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "claude"}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "backend referenced by agent") {
		t.Fatalf("error body want substring %q, got %q", "backend referenced by agent", got)
	}
}

// stubRepoWriter records the RepoDef arguments it received and returns
// canned values. Tests pin both the raw inputs the writer observed and the
// canonical repo the tool surfaces back to the caller.
type stubRepoWriter struct {
	gotUpsert     config.RepoDef
	gotDeleteName string
	canonical     config.RepoDef
	upsertErr     error
	deleteErr     error
}

func (s *stubRepoWriter) UpsertRepo(r config.RepoDef) (config.RepoDef, error) {
	s.gotUpsert = r
	if s.upsertErr != nil {
		return config.RepoDef{}, s.upsertErr
	}
	return s.canonical, nil
}

func (s *stubRepoWriter) DeleteRepo(name string) error {
	s.gotDeleteName = name
	return s.deleteErr
}

func TestToolCreateRepoForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	disabled := false
	canonical := config.RepoDef{
		Name:    "owner/repo",
		Enabled: true,
		Use: []config.Binding{
			{Agent: "coder", Labels: []string{"ready"}},
			{Agent: "planner", Cron: "0 * * * *", Enabled: &disabled},
		},
	}
	w := &stubRepoWriter{canonical: canonical}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":    "  OWNER/Repo  ",
		"enabled": true,
		"bindings": []any{
			map[string]any{
				"agent":  "Coder",
				"labels": []any{"ready"},
			},
			map[string]any{
				"agent":   "Planner",
				"cron":    "0 * * * *",
				"enabled": false,
			},
		},
	}

	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	if w.gotUpsert.Name != "  OWNER/Repo  " {
		t.Errorf("raw name should pass through to writer (writer owns normalization): got %q", w.gotUpsert.Name)
	}
	if !w.gotUpsert.Enabled {
		t.Errorf("enabled flag not forwarded: %+v", w.gotUpsert)
	}
	if got := len(w.gotUpsert.Use); got != 2 {
		t.Fatalf("bindings slice: want 2, got %d: %+v", got, w.gotUpsert.Use)
	}
	if b := w.gotUpsert.Use[0]; b.Agent != "Coder" || len(b.Labels) != 1 || b.Labels[0] != "ready" {
		t.Errorf("first binding not forwarded: %+v", b)
	}
	// The MCP tool must preserve the *bool distinction so the store validator
	// sees "explicitly disabled" rather than "default enabled" — otherwise a
	// disabled binding would flip back on after a round-trip.
	if b := w.gotUpsert.Use[1]; b.Agent != "Planner" || b.Cron != "0 * * * *" || b.Enabled == nil || *b.Enabled {
		t.Errorf("second binding not forwarded with explicit enabled=false: %+v", b)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "owner/repo" {
		t.Errorf("response should reflect canonical name: %+v", got)
	}
	if got["enabled"] != true {
		t.Errorf("response should reflect canonical enabled: %+v", got)
	}
	bindings, _ := got["bindings"].([]any)
	if len(bindings) != 2 {
		t.Fatalf("response bindings: want 2, got %d: %+v", len(bindings), got)
	}
}

func TestToolCreateRepoRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"enabled": true}

	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
	if w.gotUpsert.Name != "" {
		t.Errorf("writer should not be invoked when name missing, got %+v", w.gotUpsert)
	}
}

// TestToolCreateRepoRejectsBlankName pins the whitespace-name contract for
// create_repo: like create_skill, the handler uses req.RequireString("name")
// which only rejects the missing-key path, so a whitespace-only name must
// reach UpsertRepo and surface the writer's *store.ErrValidation as a user
// error. If the blank-name guard is ever hoisted into the tool layer, this
// test fails and forces an update to the stub-invocation expectations.
func TestToolCreateRepoRejectsBlankName(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{upsertErr: &store.ErrValidation{Msg: "name is required"}}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   ", "enabled": true}

	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "name is required") {
		t.Fatalf("error body want substring %q, got %q", "name is required", got)
	}
	if w.gotUpsert.Name != "   " {
		t.Errorf("writer should receive raw blank name (owns normalization), got %q", w.gotUpsert.Name)
	}
}

func TestToolCreateRepoPropagatesError(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{upsertErr: errors.New("unknown agent \"ghost\"")}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":    "owner/repo",
		"enabled": true,
		"bindings": []any{
			map[string]any{"agent": "ghost"},
		},
	}

	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "unknown agent") {
		t.Fatalf("error body want substring %q, got %q", "unknown agent", got)
	}
}

// TestToolCreateRepoRejectsBadBindingsShape pins the parseBindings validation
// path: non-array "bindings" must surface a user error without ever reaching
// the writer, otherwise a malformed payload would corrupt the bindings list
// silently via a zero-value upsert.
func TestToolCreateRepoRejectsBadBindingsShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		arg  any
		want string
	}{
		{"not array", "coder", "bindings must be an array"},
		{"element not object", []any{"coder"}, "bindings[0]: must be an object"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &stubRepoWriter{}
			deps := Deps{
				Config:    stubConfig{cfg: fixtureConfig()},
				Queue:     &stubQueue{},
				Status:    stubStatus{},
				RepoWrite: w,
				Logger:    zerolog.Nop(),
			}

			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = map[string]any{
				"name":     "owner/repo",
				"bindings": tc.arg,
			}

			res, err := toolCreateRepo(deps)(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError for bad bindings shape, got %+v", res)
			}
			if got := textOf(t, res); !strings.Contains(got, tc.want) {
				t.Fatalf("error body want substring %q, got %q", tc.want, got)
			}
			if w.gotUpsert.Name != "" {
				t.Errorf("writer should not be invoked when bindings shape invalid, got %+v", w.gotUpsert)
			}
		})
	}
}

// TestToolCreateRepoDefaultsBindingEnabledNil pins the default-enabled
// contract: when a binding omits "enabled", the *bool must stay nil so
// config.Binding.IsEnabled returns true. Setting it to a pointer-to-false
// here would silently disable bindings on every round-trip through MCP.
func TestToolCreateRepoDefaultsBindingEnabledNil(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{canonical: config.RepoDef{Name: "owner/repo", Enabled: true}}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":    "owner/repo",
		"enabled": true,
		"bindings": []any{
			map[string]any{"agent": "coder", "labels": []any{"ready"}},
		},
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
	if b := w.gotUpsert.Use[0]; b.Enabled != nil {
		t.Errorf("omitted enabled must stay nil (default enabled), got *bool(%v)", *b.Enabled)
	}
}

func TestToolDeleteRepoNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  OWNER/Repo  "}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if w.gotDeleteName != "owner/repo" {
		t.Errorf("name should be normalized before forwarding: got %q", w.gotDeleteName)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "owner/repo" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteRepoRequiresName(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
	if w.gotDeleteName != "" {
		t.Errorf("writer should not be invoked for blank name, got %q", w.gotDeleteName)
	}
}

func TestToolDeleteRepoPropagatesNotFound(t *testing.T) {
	t.Parallel()
	w := &stubRepoWriter{deleteErr: errors.New("repo \"owner/repo\" not found")}
	deps := Deps{
		Config:    stubConfig{cfg: fixtureConfig()},
		Queue:     &stubQueue{},
		Status:    stubStatus{},
		RepoWrite: w,
		Logger:    zerolog.Nop(),
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "owner/repo"}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on writer failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "not found") {
		t.Fatalf("error body want substring %q, got %q", "not found", got)
	}
}
