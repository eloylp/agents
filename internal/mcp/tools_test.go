package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	daemonconfig "github.com/eloylp/agents/internal/daemon/config"
	daemonfleet "github.com/eloylp/agents/internal/daemon/fleet"
	daemonrepos "github.com/eloylp/agents/internal/daemon/repos"
	daemonrunners "github.com/eloylp/agents/internal/daemon/runners"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

func fixtureConfig() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{MaxBodyBytes: 1 << 20},
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude", Models: []string{"opus", "sonnet"}, Healthy: true, TimeoutSeconds: 60},
				"codex":  {Command: "codex", Healthy: false},
			},
		},
		Skills: map[string]fleet.Skill{
			"testing":  {Prompt: "write good tests"},
			"security": {Prompt: "audit inputs"},
		},
		Agents: []fleet.Agent{
			{Name: "coder", Backend: "claude", Skills: []string{"testing"}, Description: "writes code"},
			{Name: "reviewer", Backend: "claude", AllowDispatch: true, Description: "reviews code"},
		},
		Repos: []fleet.Repo{
			{Name: "owner/one", Enabled: true, Use: []fleet.Binding{
				{Agent: "coder", Labels: []string{"bug"}},
				{Agent: "reviewer", Cron: "0 * * * *"},
			}},
			{Name: "owner/two", Enabled: false},
		},
	}
}

// testDB creates a temporary SQLite database seeded with the same entities as
// fixtureConfig so that tools reading from the DB see consistent data.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.ImportAll(
		db,
		[]fleet.Agent{
			{Name: "coder", Backend: "claude", Skills: []string{"testing"}, Prompt: "code", Description: "writes code", CanDispatch: []string{}},
			{Name: "reviewer", Backend: "claude", Prompt: "review", AllowDispatch: true, Description: "reviews code", Skills: []string{}, CanDispatch: []string{}},
		},
		[]fleet.Repo{
			{Name: "owner/one", Enabled: true, Use: []fleet.Binding{
				{Agent: "coder", Labels: []string{"bug"}},
				{Agent: "reviewer", Cron: "0 * * * *"},
			}},
			{Name: "owner/two", Enabled: false, Use: []fleet.Binding{}},
		},
		map[string]fleet.Skill{
			"testing":  {Prompt: "write good tests"},
			"security": {Prompt: "audit inputs"},
		},
		map[string]fleet.Backend{
			"claude": {Command: "claude", Models: []string{"opus", "sonnet"}, Healthy: true, TimeoutSeconds: 60},
			"codex":  {Command: "codex"},
		},
		nil,
		nil,
	); err != nil {
		t.Fatalf("seed test db: %v", err)
	}
	return db
}

// testFixture builds a Deps backed by real components for an MCP tool
// test: a tempdir SQLite seeded by fixtureConfig, a real *store.Store,
// real fleet/repos/config handlers, a real workflow.DataChannels and
// Engine, and a real observe.Store. Tests can read through deps.Store
// to verify persisted writes, drain deps.Channels.EventChan() to assert
// enqueued events, and call methods on deps.Observe directly to seed
// observability records.
//
// Stays in package mcp (not mcp_test) so it can construct Deps directly;
// the wiring goes through store + handler packages, none of which
// imports internal/daemon, so there is no import cycle.
func testFixture(t *testing.T) Deps {
	t.Helper()
	return testFixtureWithConfig(t, fixtureConfig())
}

// testFixtureWithConfig is testFixture with an explicit config. The
// store is still seeded from fixtureConfig, callers that need
// divergent state should call deps.Store.Upsert*() after construction.
func testFixtureWithConfig(t *testing.T, cfg *config.Config) Deps {
	t.Helper()
	db := testDB(t)
	st := store.New(db)
	channels := workflow.NewDataChannels(8, st)
	obs := observe.NewStore(db)
	engine := workflow.NewEngine(st, cfg.Daemon.Processor, channels, zerolog.Nop())
	sched, err := scheduler.NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	maxBody := cfg.Daemon.HTTP.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	fleetH := daemonfleet.New(st, maxBody, sched, obs, zerolog.Nop())
	reposH := daemonrepos.New(st, maxBody, zerolog.Nop())
	configH := daemonconfig.New(st, cfg.Daemon, zerolog.Nop())
	runnersH := daemonrunners.New(st, channels, obs, zerolog.Nop())
	return Deps{
		Store:        st,
		DaemonConfig: cfg.Daemon,
		StatusJSON: func() ([]byte, error) {
			// Mirror the wire shape internal/daemon.Daemon.StatusJSON returns
			// so MCP tool tests can assert on the same keys without booting
			// a full *daemon.Daemon (which would create an import cycle).
			return []byte(`{"status":"ok","uptime_seconds":0,"queues":{"events":{"buffered":0,"capacity":8}},"agents":[],"orphaned_agents":{"count":0}}`), nil
		},
		Channels: channels,
		Observe:  obs,
		Engine:   engine,
		Fleet:    fleetH,
		Repos:    reposH,
		Config:   configH,
		RunnersH: runnersH,
		Logger:   zerolog.Nop(),
	}
}

// agentByName reads a single agent from the test store by name. Used by
// write tests to verify that the tool persisted the expected fields.
func agentByName(t *testing.T, st *store.Store, name string) (fleet.Agent, bool) {
	t.Helper()
	agents, err := st.ReadAgents()
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	key := fleet.NormalizeAgentName(name)
	for _, a := range agents {
		if a.Name == key {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// drainQueue returns up to n events from the channel without blocking past
// the timeout. Used by trigger_agent tests to assert what was enqueued.
func drainQueue(t *testing.T, dc *workflow.DataChannels, n int) []workflow.Event {
	t.Helper()
	out := make([]workflow.Event, 0, n)
	deadline := time.After(100 * time.Millisecond)
	for len(out) < n {
		select {
		case qe := <-dc.EventChan():
			out = append(out, qe.Event)
		case <-deadline:
			return out
		}
	}
	return out
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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	deps := testFixture(t)

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
	// All trigger fields are always included (non-sparse shape), empty ones
	// just carry empty values. Consumers rely on a stable field set.
	labelBinding := bindings[0].(map[string]any)
	cronBinding := bindings[1].(map[string]any)
	if got, _ := labelBinding["labels"].([]any); len(got) == 0 {
		t.Errorf("label binding should carry labels: %+v", labelBinding)
	}
	if labelBinding["cron"] != "" {
		t.Errorf("label binding cron should be empty string: %+v", labelBinding)
	}
	if cronBinding["cron"] != "0 * * * *" {
		t.Errorf("cron binding should carry cron: %+v", cronBinding)
	}
	if got, _ := cronBinding["labels"].([]any); len(got) != 0 {
		t.Errorf("cron binding labels should be empty: %+v", cronBinding)
	}
}

func TestToolGetRepo(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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

	// Disabled repos still resolve, callers decide what to do with enabled=false.
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
	deps := testFixture(t)

	res, err := toolGetStatus(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Status JSON is the same payload GET /status emits, assert the wire
	// shape carries the expected top-level fields rather than pinning an
	// exact string, which would break on every status-shape change.
	var got map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("status body is not valid JSON: %v", err)
	}
	for _, key := range []string{"status", "uptime_seconds", "queues", "agents", "orphaned_agents"} {
		if _, ok := got[key]; !ok {
			t.Errorf("status JSON missing %q: %+v", key, got)
		}
	}
}

func TestToolTriggerAgentSuccess(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
	events := drainQueue(t, deps.Channels, 1)
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
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder", "repo": "owner/unknown"}

	res, err := toolTriggerAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown repo, got %+v", res)
	}
	if got := drainQueue(t, deps.Channels, 1); len(got) != 0 {
		t.Fatalf("queue should not receive events for invalid repos, got %+v", got)
	}
}

func TestToolTriggerAgentRejectsDisabledRepo(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
	deps := testFixture(t)

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

// seedEvent inserts an event row directly into the SQLite events table so
// observe.Store.ListEvents reads it back deterministically. The Store's own
// RecordEvent is async (goroutine), which would race with the immediate
// ListEvents call our tool tests need.
func seedEvent(t *testing.T, db *sql.DB, ev observe.TimestampedEvent) {
	t.Helper()
	payload, _ := json.Marshal(ev.Payload)
	if _, err := db.Exec(
		`INSERT INTO events (id, at, repo, kind, number, actor, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.At, ev.Repo, ev.Kind, ev.Number, ev.Actor, string(payload),
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}
}

// seedSpan inserts a trace span row directly into the SQLite traces table.
// Same async-vs-sync rationale as seedEvent.
func seedSpan(t *testing.T, db *sql.DB, sp observe.Span) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO traces (span_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sp.SpanID, sp.RootEventID, sp.ParentSpanID, sp.Agent, sp.Backend, sp.Repo, sp.Number,
		sp.EventKind, sp.InvokedBy, sp.DispatchDepth, sp.QueueWaitMs, sp.ArtifactsCount, sp.Summary,
		sp.StartedAt, sp.FinishedAt, sp.DurationMs, sp.Status, sp.ErrorMsg,
	); err != nil {
		t.Fatalf("seed span: %v", err)
	}
}

func TestToolListEvents(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	seedEvent(t, deps.Store.DB(), observe.TimestampedEvent{ID: "e1", Kind: "issues.labeled", Repo: "owner/one", At: time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)})
	seedEvent(t, deps.Store.DB(), observe.TimestampedEvent{ID: "e2", Kind: "agents.run", Repo: "owner/one", At: time.Date(2026, 4, 20, 10, 5, 0, 0, time.UTC)})

	res, err := toolListEvents(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 || got[0]["id"] != "e1" || got[1]["id"] != "e2" {
		t.Fatalf("unexpected events payload: %+v", got)
	}
}

func TestToolListEventsSinceFilter(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	seedEvent(t, deps.Store.DB(), observe.TimestampedEvent{ID: "old", At: time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC), Kind: "agents.run", Repo: "owner/one"})
	seedEvent(t, deps.Store.DB(), observe.TimestampedEvent{ID: "new", At: time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC), Kind: "agents.run", Repo: "owner/one"})

	// since=10:00 should drop the 09:00 event but keep the 11:00 event.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"since": "2026-04-20T10:00:00Z"}
	res, err := toolListEvents(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 1 || got[0]["id"] != "new" {
		t.Fatalf("since filter expected only new event, got %+v", got)
	}

	// Unparseable since should fall back to no filter rather than erroring,
	// matching GET /events.
	req.Params.Arguments = map[string]any{"since": "not-a-time"}
	res, err = toolListEvents(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decodeText(t, res, &got)
	if len(got) != 2 {
		t.Fatalf("unparseable since should fall back to no filter, got %+v", got)
	}
}

func TestToolListEventsNilSlice(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	res, err := toolListEvents(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nil slice from the store should serialise as [] not null, easier for
	// LLM clients that don't distinguish the two.
	got := textOf(t, res)
	if got != "[]" {
		t.Fatalf("expected []\\n, got %q", got)
	}
}

func TestToolListTraces(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	now := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	seedSpan(t, deps.Store.DB(), observe.Span{SpanID: "s1", Agent: "coder", Status: "success", StartedAt: now, FinishedAt: now.Add(time.Second)})
	seedSpan(t, deps.Store.DB(), observe.Span{SpanID: "s2", Agent: "reviewer", Status: "error", StartedAt: now.Add(time.Minute), FinishedAt: now.Add(time.Minute + time.Second)})

	res, err := toolListTraces(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []map[string]any
	decodeText(t, res, &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 traces, got %+v", got)
	}
}

func TestToolGetTrace(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	now := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	seedSpan(t, deps.Store.DB(), observe.Span{SpanID: "s1", RootEventID: "root-1", Agent: "coder", StartedAt: now, FinishedAt: now.Add(time.Second)})

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
	deps := testFixture(t)

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
	deps := testFixture(t)
	deps.Observe.RecordSteps("s1", []workflow.TraceStep{{ToolName: "read_file", DurationMs: 42}})

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

func TestToolGetTracePromptReturnsBody(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	now := time.Now().UTC()
	deps.Observe.RecordSpan(workflow.SpanInput{
		SpanID: "sp-prompt", RootEventID: "ev-prompt",
		Agent: "coder", Backend: "claude", Repo: "owner/one", EventKind: "issues.labeled",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Status: "success",
		Prompt: "system: do the thing\n\nuser: please",
	})
	// RecordSpan persists asynchronously; poll briefly until visible.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := deps.Observe.PromptForSpan("sp-prompt"); err == nil && got != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"span_id": "sp-prompt"}
	res, err := toolGetTracePrompt(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	body := textOf(t, res)
	if body != "system: do the thing\n\nuser: please" {
		t.Fatalf("body = %q, want round-trip of recorded prompt", body)
	}
}

func TestToolGetTracePromptMissingErrors(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"span_id": "nope"}
	res, err := toolGetTracePrompt(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for unknown span")
	}
}

func TestToolGetTracePromptRequiresSpanID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	res, err := toolGetTracePrompt(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when span_id is missing")
	}
}

func TestToolGetGraphSeedsNodesFromFleetAndEdges(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Insert directly so the edge is visible immediately, RecordDispatch
	// writes asynchronously, which races with toolGetGraph below.
	if _, err := deps.Store.DB().Exec(
		`INSERT INTO dispatch_history (from_agent, to_agent, repo, number, reason) VALUES (?,?,?,?,?)`,
		"coder", "ghost", "owner/one", 7, "followup",
	); err != nil {
		t.Fatalf("seed dispatch: %v", err)
	}

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
	deps := testFixture(t)

	res, err := toolGetDispatches(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A fresh engine has zero counters; assert the wire shape carries the
	// expected fields rather than specific values (which would lock the
	// test to engine internals).
	var got map[string]any
	decodeText(t, res, &got)
	for _, key := range []string{"requested_total", "enqueued"} {
		if _, ok := got[key]; !ok {
			t.Errorf("dispatch stats missing %q: %+v", key, got)
		}
	}
}

func TestToolGetMemorySuccess(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	if err := deps.Store.WriteMemoryRaw("coder", "owner_one", "# hello\n"); err != nil {
		t.Fatalf("seed memory: %v", err)
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
}

func TestToolGetMemoryMissing(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
	deps := testFixture(t)

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
}

func TestToolGetMemoryRequiresBothArgs(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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

func TestToolGetConfigReturnsRedactedJSON(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	res, err := toolGetConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Body is the same payload GET /config emits, assert it parses as JSON
	// and carries the expected top-level fields.
	var got map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("config body is not valid JSON: %v", err)
	}
	for _, key := range []string{"daemon", "agents", "skills", "repos"} {
		if _, ok := got[key]; !ok {
			t.Errorf("config JSON missing %q: %+v", key, got)
		}
	}
}

func TestToolExportConfigReturnsYAML(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	res, err := toolExportConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := textOf(t, res)
	if body == "" {
		t.Fatalf("expected non-empty YAML, got empty")
	}
	// Smoke check: the export should at least mention the seeded entities.
	for _, want := range []string{"coder", "reviewer", "claude"} {
		if !strings.Contains(body, want) {
			t.Errorf("export YAML missing %q: %s", want, body)
		}
	}
}

func TestToolImportConfigPersistsYAMLAndMode(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// Round-trip the fixture through export → import. The merge mode is the
	// default; it should upsert without disturbing existing entries.
	res, err := toolExportConfig(deps)(context.Background(), mcpgo.CallToolRequest{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	body := textOf(t, res)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": body, "mode": "replace"}
	res, err = toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %s", textOf(t, res))
	}
	var got map[string]int
	decodeText(t, res, &got)
	if got["agents"] < 1 || got["skills"] < 1 || got["repos"] < 1 || got["backends"] < 1 {
		t.Errorf("counts wire shape: got %+v", got)
	}
	// Verify the entities are still present after the replace import.
	if _, ok := agentByName(t, deps.Store, "coder"); !ok {
		t.Errorf("coder agent missing after replace import")
	}
}

func TestToolImportConfigDefaultsMode(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// Empty YAML body with default mode should be accepted without error.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": "skills: {}\n"}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %s", textOf(t, res))
	}
}

func TestToolImportConfigRequiresYAML(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when yaml argument missing, got %+v", res)
	}
}

func TestToolImportConfigPropagatesError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// Malformed YAML triggers a real parse error from the importer.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"yaml": "agents: [not-a-list-of-objects", "mode": "replace"}

	res, err := toolImportConfig(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when importer fails, got %+v", res)
	}
}

// ── Writer-side helpers ──────────────────────────────────────────────────────

func skillByName(t *testing.T, st *store.Store, name string) (fleet.Skill, bool) {
	t.Helper()
	skills, err := st.ReadSkills()
	if err != nil {
		t.Fatalf("read skills: %v", err)
	}
	sk, ok := skills[fleet.NormalizeSkillName(name)]
	return sk, ok
}

func backendByName(t *testing.T, st *store.Store, name string) (fleet.Backend, bool) {
	t.Helper()
	bes, err := st.ReadBackends()
	if err != nil {
		t.Fatalf("read backends: %v", err)
	}
	b, ok := bes[fleet.NormalizeBackendName(name)]
	return b, ok
}

func repoByName(t *testing.T, st *store.Store, name string) (fleet.Repo, bool) {
	t.Helper()
	repos, err := st.ReadRepos()
	if err != nil {
		t.Fatalf("read repos: %v", err)
	}
	key := fleet.NormalizeRepoName(name)
	for _, r := range repos {
		if r.Name == key {
			return r, true
		}
	}
	return fleet.Repo{}, false
}

// ── create_agent / delete_agent ──────────────────────────────────────────────

func TestToolCreateAgentForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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

	// Verify the agent was persisted with the canonical (normalized) name.
	persisted, ok := agentByName(t, deps.Store, "linter")
	if !ok {
		t.Fatal("linter not found in store after create_agent")
	}
	if persisted.Backend != "claude" || persisted.Prompt != "audit" {
		t.Errorf("persisted agent missing fields: %+v", persisted)
	}
	if !persisted.AllowDispatch || len(persisted.CanDispatch) != 1 || persisted.CanDispatch[0] != "coder" {
		t.Errorf("dispatch fields not persisted: %+v", persisted)
	}
	if len(persisted.Skills) != 1 || persisted.Skills[0] != "security" {
		t.Errorf("skills slice not persisted: %+v", persisted.Skills)
	}

	// Verify the response wire shape mirrors the canonical entity.
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
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"backend": "claude"}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
}

func TestToolCreateAgentPropagatesError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// A blank/whitespace name triggers the real *store.ErrValidation from
	// UpsertAgent, same path as REST, surfaced as a tool-level error.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   ", "backend": "claude"}

	res, err := toolCreateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on validation failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "name is required") {
		t.Fatalf("error body want substring %q, got %q", "name is required", got)
	}
}

func TestToolDeleteAgentNormalizesAndForwardsCascade(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Seed an extra agent that has no bindings, we can delete it without cascade.
	if _, err := deps.Fleet.UpsertAgent(fleet.Agent{Name: "linter", Backend: "claude", Prompt: "x"}); err != nil {
		t.Fatalf("seed linter: %v", err)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  Linter  "}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	// Deleted from the store?
	if _, ok := agentByName(t, deps.Store, "linter"); ok {
		t.Errorf("linter should have been removed from the store")
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "linter" || got["cascade"] != false {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteAgentRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
}

func TestToolDeleteAgentPropagatesConflict(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// "coder" has a binding in fixtureConfig, delete-without-cascade should
	// surface the real *store.ErrConflict.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "coder"}

	res, err := toolDeleteAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on conflict, got %+v", res)
	}
	if _, ok := agentByName(t, deps.Store, "coder"); !ok {
		t.Errorf("coder should still exist after a conflicting delete")
	}
}

// ── create_skill / update_skill / delete_skill ───────────────────────────────

func TestToolCreateSkillForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "  Hardening  ",
		"prompt": "  audit inputs carefully  ",
	}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	// Persisted under canonical (lowercased, trimmed) name with trimmed body.
	sk, ok := skillByName(t, deps.Store, "hardening")
	if !ok {
		t.Fatal("hardening skill missing after create")
	}
	if sk.Prompt != "audit inputs carefully" {
		t.Errorf("prompt should be trimmed by store: %q", sk.Prompt)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "hardening" || got["prompt"] != "audit inputs carefully" {
		t.Errorf("response should reflect canonical entity: %+v", got)
	}
}

func TestToolCreateSkillRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"prompt": "body"}

	res, err := toolCreateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
}

func TestToolCreateSkillRejectsBlankName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// A whitespace name reaches UpsertSkill which surfaces *store.ErrValidation.
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
}

func TestToolDeleteSkillNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// "security" is seeded but not referenced by any agent, safe to delete.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  Security  "}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if _, ok := skillByName(t, deps.Store, "security"); ok {
		t.Errorf("security skill should have been removed")
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "security" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteSkillRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
}

func TestToolDeleteSkillPropagatesConflict(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// "testing" is referenced by the "coder" agent, deletion conflicts.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "testing"}

	res, err := toolDeleteSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on conflict, got %+v", res)
	}
	if _, ok := skillByName(t, deps.Store, "testing"); !ok {
		t.Errorf("testing skill should still exist after a conflicting delete")
	}
}

// ── create_backend / update_backend / delete_backend ─────────────────────────

func TestToolCreateBackendForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Custom backend names need a local_model_url to satisfy validation , 
	// otherwise only the built-in claude/codex/claude_local names are accepted.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":             "  LocalLlama  ",
		"command":          "claude",
		"local_model_url":  "http://localhost:8080",
		"models":           []any{"qwen3-coder"},
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

	persisted, ok := backendByName(t, deps.Store, "localllama")
	if !ok {
		t.Fatal("localllama backend missing after create")
	}
	if persisted.Command != "claude" {
		t.Errorf("command not persisted: %+v", persisted)
	}
	if persisted.TimeoutSeconds != 600 || persisted.MaxPromptChars != 12000 {
		t.Errorf("runtime settings not persisted: (%d, %d)", persisted.TimeoutSeconds, persisted.MaxPromptChars)
	}
	if len(persisted.Models) != 1 || persisted.Models[0] != "qwen3-coder" {
		t.Errorf("models slice not persisted: %+v", persisted.Models)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "localllama" {
		t.Errorf("response should reflect canonical name: %+v", got)
	}
}

func TestToolCreateBackendRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"command": "claude"}

	res, err := toolCreateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
}

func TestToolCreateBackendRejectsBlankName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
}

func TestToolDeleteBackendNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Seed a backend with no agents referencing it so the delete is allowed.
	if _, _, err := deps.Fleet.UpsertBackend("LocalLlama", fleet.Backend{Command: "claude", LocalModelURL: "http://localhost:8080", TimeoutSeconds: 60, MaxPromptChars: 1000}); err != nil {
		t.Fatalf("seed backend: %v", err)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  LocalLlama  "}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if _, ok := backendByName(t, deps.Store, "localllama"); ok {
		t.Errorf("localllama backend should have been removed")
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "localllama" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteBackendRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
}

func TestToolDeleteBackendPropagatesConflict(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// "claude" is referenced by both seeded agents, delete should conflict.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "claude"}

	res, err := toolDeleteBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on conflict, got %+v", res)
	}
	if _, ok := backendByName(t, deps.Store, "claude"); !ok {
		t.Errorf("claude backend should still exist after a conflicting delete")
	}
}

// ── create_repo / delete_repo ────────────────────────────────────────────────

func TestToolCreateRepoForwardsAndReturnsCanonical(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
				"agent":   "Reviewer",
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

	persisted, ok := repoByName(t, deps.Store, "owner/repo")
	if !ok {
		t.Fatal("owner/repo missing after create")
	}
	if !persisted.Enabled {
		t.Errorf("enabled flag not persisted: %+v", persisted)
	}
	if got := len(persisted.Use); got != 2 {
		t.Fatalf("bindings: want 2, got %d: %+v", got, persisted.Use)
	}
	if b := persisted.Use[0]; b.Agent != "coder" || len(b.Labels) != 1 || b.Labels[0] != "ready" {
		t.Errorf("first binding wrong: %+v", b)
	}
	// The MCP tool must preserve the *bool distinction so the store sees
	// "explicitly disabled" rather than "default enabled", otherwise a
	// disabled binding would flip back on after a round-trip.
	if b := persisted.Use[1]; b.Agent != "reviewer" || b.Cron != "0 * * * *" || b.Enabled == nil || *b.Enabled {
		t.Errorf("second binding wrong (expected explicit enabled=false): %+v", b)
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "owner/repo" || got["enabled"] != true {
		t.Errorf("response should reflect canonical entity: %+v", got)
	}
	bindings, _ := got["bindings"].([]any)
	if len(bindings) != 2 {
		t.Fatalf("response bindings: want 2, got %d: %+v", len(bindings), got)
	}
}

func TestToolCreateRepoRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"enabled": true}

	res, err := toolCreateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
}

func TestToolCreateRepoRejectsBlankName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

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
}

func TestToolCreateRepoPropagatesError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// Binding to a non-existent agent triggers the real binding-validation
	// error from UpsertRepo.
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
		t.Fatalf("expected IsError on validation failure, got %+v", res)
	}
	if got := textOf(t, res); !strings.Contains(got, "ghost") {
		t.Fatalf("error body should mention unknown agent: got %q", got)
	}
	if _, ok := repoByName(t, deps.Store, "owner/repo"); ok {
		t.Errorf("owner/repo should NOT have been persisted on validation failure")
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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := testFixture(t)
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = map[string]any{
				"name":     "owner/badshape",
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
			if _, ok := repoByName(t, deps.Store, "owner/badshape"); ok {
				t.Errorf("repo must not be persisted when bindings shape invalid")
			}
		})
	}
}

// TestToolCreateRepoRejectsBadBindingFieldTypes pins the strict type contract
// for nested binding fields. REST decodes POST /repos through json.Unmarshal
// into storeBindingJSON, which rejects wrong JSON types; parseBindings must
// refuse the same payloads rather than silently coercing them. In particular,
// `{"enabled":"false"}` must NOT be treated as omitted, that would leave
// Binding.Enabled=nil (default enabled) and silently flip the caller's intent.
func TestToolCreateRepoRejectsBadBindingFieldTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		binding map[string]any
		want    string
	}{
		{"enabled not boolean (string)", map[string]any{"agent": "coder", "enabled": "false"}, "bindings[0].enabled must be a boolean"},
		{"enabled not boolean (number)", map[string]any{"agent": "coder", "enabled": 0}, "bindings[0].enabled must be a boolean"},
		{"agent not string", map[string]any{"agent": 42}, "bindings[0].agent must be a string"},
		{"cron not string", map[string]any{"agent": "coder", "cron": 15}, "bindings[0].cron must be a string"},
		{"labels not array", map[string]any{"agent": "coder", "labels": "ready"}, "bindings[0].labels must be an array"},
		{"labels element not string", map[string]any{"agent": "coder", "labels": []any{"ready", 2}}, "bindings[0].labels[1] must be a string"},
		{"events not array", map[string]any{"agent": "coder", "events": "push"}, "bindings[0].events must be an array"},
		{"events element not string", map[string]any{"agent": "coder", "events": []any{true}}, "bindings[0].events[0] must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := testFixture(t)
			req := mcpgo.CallToolRequest{}
			req.Params.Arguments = map[string]any{
				"name":     "owner/badbinding",
				"bindings": []any{tc.binding},
			}
			res, err := toolCreateRepo(deps)(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError for bad binding field, got %+v", res)
			}
			if got := textOf(t, res); !strings.Contains(got, tc.want) {
				t.Fatalf("error body want substring %q, got %q", tc.want, got)
			}
			if _, ok := repoByName(t, deps.Store, "owner/badbinding"); ok {
				t.Errorf("repo must not be persisted when binding fields invalid")
			}
		})
	}
}

// TestToolCreateRepoDefaultsBindingEnabledNil pins the default-enabled
// contract: when a binding omits "enabled", the *bool must stay nil so
// fleet.Binding.IsEnabled returns true. Setting it to a pointer-to-false
// here would silently disable bindings on every round-trip through MCP.
func TestToolCreateRepoDefaultsBindingEnabledNil(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":    "owner/defaultenabled",
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
	persisted, ok := repoByName(t, deps.Store, "owner/defaultenabled")
	if !ok {
		t.Fatal("repo not persisted")
	}
	if len(persisted.Use) != 1 {
		t.Fatalf("bindings: want 1, got %d", len(persisted.Use))
	}
	// Persisted binding should report IsEnabled() = true and have Enabled
	// either nil or *true (the store may materialise either form).
	if !persisted.Use[0].IsEnabled() {
		t.Errorf("default-enabled binding should be active, got %+v", persisted.Use[0])
	}
}

// TestToolUpdateRepoTogglesEnabledPreservingBindings verifies the partial-
// update contract: flipping repo.Enabled does NOT churn the bindings list
// (unlike create_repo, which is full-replace and would re-issue IDs).
func TestToolUpdateRepoTogglesEnabledPreservingBindings(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Capture the binding IDs on the fixture's enabled repo before the update.
	before, ok := repoByName(t, deps.Store, "owner/one")
	if !ok {
		t.Fatal("owner/one missing in fixture")
	}
	if !before.Enabled {
		t.Fatal("owner/one should start enabled")
	}
	beforeIDs := make([]int64, 0, len(before.Use))
	for _, b := range before.Use {
		beforeIDs = append(beforeIDs, b.ID)
	}
	if len(beforeIDs) == 0 {
		t.Fatal("fixture repo should have bindings to assert ID preservation")
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  OWNER/One  ", "enabled": false}

	res, err := toolUpdateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}

	after, ok := repoByName(t, deps.Store, "owner/one")
	if !ok {
		t.Fatal("owner/one missing after update")
	}
	if after.Enabled {
		t.Errorf("repo should be disabled after update, got enabled=true")
	}
	if len(after.Use) != len(before.Use) {
		t.Fatalf("bindings count changed: before=%d after=%d", len(before.Use), len(after.Use))
	}
	for i, b := range after.Use {
		if b.ID != beforeIDs[i] {
			t.Errorf("binding %d: ID changed %d → %d (update_repo must preserve IDs)", i, beforeIDs[i], b.ID)
		}
	}

	var got map[string]any
	decodeText(t, res, &got)
	if got["name"] != "owner/one" || got["enabled"] != false {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolUpdateRepoRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"enabled": true}
	res, err := toolUpdateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when name missing, got %+v", res)
	}
}

func TestToolUpdateRepoRequiresEnabled(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "owner/one"}
	res, err := toolUpdateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError when enabled missing, got %+v", res)
	}
}

func TestToolUpdateRepoPropagatesNotFound(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "owner/never-existed", "enabled": true}
	res, err := toolUpdateRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for unknown repo, got %+v", res)
	}
}

func TestToolDeleteRepoNormalizesAndForwards(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	// Seed a separate enabled repo so the fleet keeps at least one enabled
	// repo after we delete owner/two (the disabled fixture repo isn't a
	// safe test target, the fleet must keep ≥1 enabled repo).
	if _, err := deps.Repos.UpsertRepo(fleet.Repo{Name: "owner/three", Enabled: true}); err != nil {
		t.Fatalf("seed owner/three: %v", err)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "  OWNER/Three  "}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	if _, ok := repoByName(t, deps.Store, "owner/three"); ok {
		t.Errorf("owner/three should have been removed")
	}
	var got map[string]any
	decodeText(t, res, &got)
	if got["status"] != "deleted" || got["name"] != "owner/three" {
		t.Errorf("response shape: %+v", got)
	}
}

func TestToolDeleteRepoRequiresName(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "   "}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for blank name, got %+v", res)
	}
}

func TestToolDeleteRepoIsIdempotent(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	// The store delete is idempotent for unknown names, a missing repo
	// is treated as already-deleted rather than a 404. The MCP tool
	// surfaces the same shape ("status":"deleted") so callers can issue a
	// retry without special-casing the missing-row response.
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "owner/never-existed"}

	res, err := toolDeleteRepo(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected idempotent success, got error: %s", textOf(t, res))
	}
}

// ── create_binding / update_binding / delete_binding ────────────────────────

// firstBindingID returns the first binding ID for the given repo. The fixture
// gives owner/one two bindings; their IDs are assigned at insert time.
func firstBindingID(t *testing.T, deps Deps, repoName string) int64 {
	t.Helper()
	r, ok := repoByName(t, deps.Store, repoName)
	if !ok || len(r.Use) == 0 {
		t.Fatalf("repo %q has no bindings", repoName)
	}
	return r.Use[0].ID
}

func TestToolCreateBindingForwardsAndReturnsID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   "owner/one",
		"agent":  "coder",
		"labels": []any{"ai:fix"},
	}
	res, err := toolCreateBinding(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	var out map[string]any
	decodeText(t, res, &out)
	if id, _ := out["id"].(float64); id <= 0 {
		t.Errorf("id should be > 0, got %v", out["id"])
	}
	// Verify it's persisted on owner/one.
	r, ok := repoByName(t, deps.Store, "owner/one")
	if !ok {
		t.Fatal("owner/one missing")
	}
	found := false
	for _, b := range r.Use {
		if b.Agent == "coder" && len(b.Labels) == 1 && b.Labels[0] == "ai:fix" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("new ai:fix binding not persisted on owner/one: %+v", r.Use)
	}
}

func TestToolCreateBindingRequiresRepoAndAgent(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"agent": "coder"}
	res, err := toolCreateBinding(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError for missing repo, got %+v", res)
	}
}

func TestToolUpdateBindingForwardsID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id := firstBindingID(t, deps, "owner/one")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"id":      float64(id),
		"repo":    "owner/one",
		"agent":   "coder",
		"cron":    "0 9 * * *",
		"enabled": false,
	}
	res, err := toolUpdateBinding(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	r, _ := repoByName(t, deps.Store, "owner/one")
	var updated *fleet.Binding
	for i := range r.Use {
		if r.Use[i].ID == id {
			updated = &r.Use[i]
			break
		}
	}
	if updated == nil {
		t.Fatalf("binding %d missing after update", id)
	}
	if updated.Cron != "0 9 * * *" {
		t.Errorf("cron not updated: %+v", updated)
	}
	if updated.Enabled == nil || *updated.Enabled {
		t.Errorf("enabled=false should persist as explicit false: %+v", updated)
	}
}

func TestToolDeleteBindingForwardsID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id := firstBindingID(t, deps, "owner/one")

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"id":   float64(id),
		"repo": "owner/one",
	}
	res, err := toolDeleteBinding(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", textOf(t, res))
	}
	r, _ := repoByName(t, deps.Store, "owner/one")
	for _, b := range r.Use {
		if b.ID == id {
			t.Errorf("binding %d should have been removed", id)
		}
	}
}

// ── update_agent / update_skill / update_backend ─────────────────────────────

func TestToolUpdateAgentForwardsPatch(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":      "coder",
		"backend":   "codex",
		"allow_prs": true,
		"skills":    []any{"testing"},
	}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_agent failed: err=%v body=%s", err, textOf(t, res))
	}
	updated, ok := agentByName(t, deps.Store, "coder")
	if !ok {
		t.Fatal("coder missing after update")
	}
	if updated.Backend != "codex" {
		t.Errorf("backend not patched: %q", updated.Backend)
	}
	if !updated.AllowPRs {
		t.Errorf("allow_prs not patched")
	}
	if len(updated.Skills) != 1 || updated.Skills[0] != "testing" {
		t.Errorf("skills not patched: %+v", updated.Skills)
	}
	// Fields not in payload are preserved (description was set in seed).
	if updated.Description != "writes code" {
		t.Errorf("description should be preserved, got %q", updated.Description)
	}
}

func TestToolUpdateAgentEmptyPatchRejected(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "coder"}
	res, err := toolUpdateAgent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for empty patch")
	}
}

func TestToolUpdateSkillForwardsPatch(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":   "Security",
		"prompt": "audit",
	}
	res, err := toolUpdateSkill(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_skill failed: err=%v body=%s", err, textOf(t, res))
	}
	sk, ok := skillByName(t, deps.Store, "security")
	if !ok {
		t.Fatal("security skill missing after update")
	}
	if sk.Prompt != "audit" {
		t.Errorf("prompt not patched: %q", sk.Prompt)
	}
}

func TestToolUpdateSkillEmptyPatchRejected(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "security"}
	res, err := toolUpdateSkill(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for empty patch")
	}
}

func TestToolUpdateBackendForwardsPatch(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":            "Claude",
		"timeout_seconds": float64(900),
		"models":          []any{"opus", "sonnet"},
	}
	res, err := toolUpdateBackend(deps)(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("update_backend failed: err=%v body=%s", err, textOf(t, res))
	}
	b, ok := backendByName(t, deps.Store, "claude")
	if !ok {
		t.Fatal("claude backend missing after update")
	}
	if b.TimeoutSeconds != 900 {
		t.Errorf("timeout_seconds not patched: %d", b.TimeoutSeconds)
	}
	if len(b.Models) != 2 {
		t.Errorf("models not patched: %+v", b.Models)
	}
	// Fields not patched should be preserved (Command was "claude" in seed).
	if b.Command != "claude" {
		t.Errorf("command should be preserved, got %q", b.Command)
	}
}

func TestToolUpdateBackendNonPositiveRejected(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name":            "claude",
		"timeout_seconds": float64(0),
	}
	res, err := toolUpdateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for timeout_seconds=0")
	}
}

func TestToolUpdateBackendEmptyPatchRejected(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "claude"}
	res, err := toolUpdateBackend(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for empty patch")
	}
}
