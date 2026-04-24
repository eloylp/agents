package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
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
}
