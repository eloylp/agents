package observe

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	
	"github.com/eloylp/agents/internal/fleet"
	obstore "github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// ── helpers ────────────────────────────────────────────────────────────────

// minimalCfg builds a *config.Config sufficient for the coordinator + observe
// handler under test. The four entity sets are filled by store.ImportAll into
// each test's temp DB; this struct just supplies the daemon-level fields the
// coordinator preserves on reload.
func minimalCfg() *config.Config {
	return &config.Config{
		Daemon: config.DaemonConfig{
			HTTP: config.HTTPConfig{ListenAddr: ":0"},
			AIBackends: map[string]fleet.Backend{
				"claude": {Command: "claude", TimeoutSeconds: 60, MaxPromptChars: 1024},
			},
		},
	}
}

// testFixture holds the per-test wiring an observe.Handler needs: the
// raw SQLite handle (for memory-row seeding helpers), the observability
// pub-sub store, and the data-access facade.
type testFixture struct {
	db     *sql.DB        // raw handle, only for memory-row seeding
	events *obstore.Store // events / traces / SSE pub-sub
	store  *store.Store   // data-access facade
}

// newFixture opens a temp SQLite and creates an obstore.Store + data-
// access store on top. If cfg has populated entity sets, they are seeded
// so the observe handler's SQLite reads see them.
func newFixture(t *testing.T, cfg *config.Config) testFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { st.Close() })
	if cfg != nil && (len(cfg.Agents) > 0 || len(cfg.Repos) > 0 || len(cfg.Daemon.AIBackends) > 0 || len(cfg.Skills) > 0) {
		backends := cfg.Daemon.AIBackends
		if len(backends) == 0 {
			backends = map[string]fleet.Backend{"claude": {Command: "claude"}}
		}
		skills := cfg.Skills
		if skills == nil {
			skills = map[string]fleet.Skill{}
		}
		if err := st.ImportAll(cfg.Agents, cfg.Repos, skills, backends); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	events := obstore.NewStore(db)
	return testFixture{db: db, events: events, store: st}
}

// newTestEvents is the legacy single-arg helper kept so the test bodies
// that only touch obs need no more than a one-line change.
func newTestEvents(t *testing.T) *obstore.Store {
	t.Helper()
	return newFixture(t, nil).events
}

// newSchedulerWithStatuses builds a scheduler whose AgentStatuses()
// returns the supplied entries. The scheduler reconciles cron bindings
// from a tempdir SQLite seeded with one cron binding per (agent, repo)
// pair; RecordLastRun is then called to populate lastRun + lastStatus.
func newSchedulerWithStatuses(t *testing.T, statuses []scheduler.AgentStatus) *scheduler.Scheduler {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repos := make([]fleet.Repo, 0, len(statuses))
	agents := make([]fleet.Agent, 0, len(statuses))
	seenAgent := map[string]bool{}
	for _, st := range statuses {
		repos = append(repos, fleet.Repo{
			Name: st.Repo, Enabled: true,
			Use: []fleet.Binding{{Agent: st.Name, Cron: "0 * * * *"}},
		})
		if !seenAgent[st.Name] {
			agents = append(agents, fleet.Agent{Name: st.Name, Backend: "claude", Prompt: "p"})
			seenAgent[st.Name] = true
		}
	}
	st := store.New(db)
	if err := st.ImportAll(agents, repos, map[string]fleet.Skill{}, map[string]fleet.Backend{"claude": {Command: "claude"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sched, err := scheduler.NewScheduler(st, time.Hour, zerolog.Nop())
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	for _, st := range statuses {
		ts := time.Time{}
		if st.LastRun != nil {
			ts = *st.LastRun
		}
		sched.RecordLastRun(st.Name, st.Repo, ts, st.LastStatus)
	}
	return sched
}

// newPlainHandler builds a Handler with a tempdir-backed obs.Store. Most
// observe-handler tests don't care about scheduling, dispatch stats, or
// memory — passing nil for those slots keeps the call sites short.
func newPlainHandler(t *testing.T) *Handler {
	t.Helper()
	fx := newFixture(t, nil)
	return New(fx.events, fx.store, nil, nil, nil, zerolog.Nop())
}

// newHandlerOnStore is like newPlainHandler but reuses an existing
// obs.Store (so the test can write events / traces to it before the
// handler queries them).
func newHandlerOnStore(t *testing.T, obs *obstore.Store) *Handler {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(obs, store.New(db), nil, nil, nil, zerolog.Nop())
}

// seedMemoryReader writes (agent, repo) → content rows into db so the
// coordinator's real SQLiteMemoryReader sees them. mtimes lets tests
// override the auto-stamped updated_at on a per-key basis. The memory
// table references agents and repos, so we upsert each pair first.
func seedMemoryReader(t *testing.T, db *sql.DB, content map[string]string, mtimes map[string]time.Time) *store.MemoryReader {
	t.Helper()
	if err := store.UpsertBackend(db, "claude", fleet.Backend{Command: "claude"}); err != nil {
		t.Fatalf("seed backend: %v", err)
	}
	seenAgent := map[string]bool{}
	seenRepo := map[string]bool{}
	for key, body := range content {
		parts := strings.SplitN(key, "\x00", 2)
		agent := parts[0]
		repo := ""
		if len(parts) == 2 {
			repo = parts[1]
		}
		agent = ai.NormalizeToken(agent)
		repo = ai.NormalizeToken(repo)
		if !seenAgent[agent] {
			if err := store.UpsertAgent(db, fleet.Agent{Name: agent, Backend: "claude", Prompt: "p"}); err != nil {
				t.Fatalf("seed agent %s: %v", agent, err)
			}
			seenAgent[agent] = true
		}
		if !seenRepo[repo] {
			if err := store.UpsertRepo(db, fleet.Repo{Name: repo, Enabled: true}); err != nil {
				t.Fatalf("seed repo %s: %v", repo, err)
			}
			seenRepo[repo] = true
		}
		if err := store.WriteMemory(db, agent, repo, body); err != nil {
			t.Fatalf("seed memory %s/%s: %v", agent, repo, err)
		}
		if ts, ok := mtimes[key]; ok && !ts.IsZero() {
			if _, err := db.Exec(
				"UPDATE memory SET updated_at = ? WHERE agent = ? AND repo = ?",
				ts.UTC().Format(time.RFC3339Nano), agent, repo,
			); err != nil {
				t.Fatalf("override mtime: %v", err)
			}
		}
	}
	return store.NewMemoryReader(db)
}

// newRouter mounts h on a fresh router with an identity timeout wrapper so
// tests exercise the full route table including mux variable extraction.
func newRouter(h *Handler) *mux.Router {
	r := mux.NewRouter()
	h.RegisterRoutes(r, func(handler http.Handler) http.Handler { return handler })
	return r
}

// sseCapture is a minimal http.ResponseWriter + http.Flusher that forwards
// each Write call to a buffered channel. Lets test goroutines receive SSE
// frames without races on a shared bytes.Buffer.
type sseCapture struct {
	header http.Header
	writes chan []byte
}

func newSSECapture() *sseCapture {
	return &sseCapture{
		header: make(http.Header),
		writes: make(chan []byte, 32),
	}
}

func (c *sseCapture) Header() http.Header { return c.header }
func (c *sseCapture) WriteHeader(_ int)   {}
func (c *sseCapture) Write(b []byte) (int, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	c.writes <- cp
	return len(b), nil
}
func (c *sseCapture) Flush() {}

// mustReadSSEMsg drains one message from ch within timeout or fails the test.
func mustReadSSEMsg(t *testing.T, ch <-chan []byte, timeout time.Duration) string {
	t.Helper()
	select {
	case msg := <-ch:
		return string(msg)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for SSE message")
		return ""
	}
}

// ── /dispatches ────────────────────────────────────────────────────────────

func TestHandleDispatchesReturnsEngineStats(t *testing.T) {
	t.Parallel()
	// Build a real engine. Tests can't easily seed dispatch counters (they
	// are per-Dispatcher atomics), so we just assert that /dispatches
	// returns a parseable DispatchStats payload backed by the engine the
	// composing daemon hands the handler.
	fx := newFixture(t, nil)
	channels := workflow.NewDataChannels(1, fx.store)
	engine := workflow.NewEngine(fx.store, minimalCfg().Daemon.Processor, channels, zerolog.Nop())
	h := New(fx.events, fx.store, nil, engine, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/dispatches", nil)
	rec := httptest.NewRecorder()
	h.HandleDispatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got workflow.DispatchStats
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (workflow.DispatchStats{}) {
		t.Errorf("want zero stats from a fresh engine, got %+v", got)
	}
}

func TestHandleDispatchesZeroWhenNoEngine(t *testing.T) {
	t.Parallel()
	h := newPlainHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/dispatches", nil)
	rec := httptest.NewRecorder()
	h.HandleDispatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got workflow.DispatchStats
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != (workflow.DispatchStats{}) {
		t.Errorf("want zero stats, got %+v", got)
	}
}

// ── /events ────────────────────────────────────────────────────────────────

func TestHandleEventsReturnsStoredEvents(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	now := time.Now().UTC()
	obs.RecordEvent(now, workflow.Event{ID: "evt-1", Kind: "issues.labeled", Repo: workflow.RepoRef{FullName: "owner/repo"}, Number: 42, Actor: "user"})
	obs.RecordEvent(now.Add(time.Second), workflow.Event{ID: "evt-2", Kind: "push", Repo: workflow.RepoRef{FullName: "owner/repo"}, Actor: "bot"})
	time.Sleep(50 * time.Millisecond) // wait for async DB writes

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rec := httptest.NewRecorder()
	h.HandleEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var events []eventJSON
	if err := json.NewDecoder(rec.Body).Decode(&events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].ID != "evt-1" || events[1].ID != "evt-2" {
		t.Fatalf("unexpected event IDs: %v %v", events[0].ID, events[1].ID)
	}
}

func TestHandleEventsSinceFilter(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	base := time.Now().UTC()
	obs.RecordEvent(base, workflow.Event{ID: "old", Kind: "push"})
	obs.RecordEvent(base.Add(2*time.Second), workflow.Event{ID: "new", Kind: "push"})
	time.Sleep(50 * time.Millisecond)

	since := base.Add(time.Second).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/events?since="+since, nil)
	rec := httptest.NewRecorder()
	h.HandleEvents(rec, req)

	var events []eventJSON
	_ = json.NewDecoder(rec.Body).Decode(&events)
	if len(events) != 1 || events[0].ID != "new" {
		t.Fatalf("want only 'new' event after filter, got %v", events)
	}
}

// ── SSE stream handlers ────────────────────────────────────────────────────

// TestHandleSSEStreams verifies the three SSE stream handlers
// (events/stream, traces/stream, memory/stream) using a table-driven
// approach. Each sub-test connects a subscriber, reads the immediate
// ": connected" comment, publishes a message, and verifies it arrives.
func TestHandleSSEStreams(t *testing.T) {
	t.Parallel()

	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		publish func(msg []byte)
		msg     string
	}{
		{
			name:    "events/stream",
			handler: h.HandleEventsStream,
			publish: obs.EventsSSE.Publish,
			msg:     `data: {"id":"ev1"}` + "\n\n",
		},
		{
			name:    "traces/stream",
			handler: h.HandleTracesStream,
			publish: obs.TracesSSE.Publish,
			msg:     `data: {"span_id":"sp1"}` + "\n\n",
		},
		{
			name:    "memory/stream",
			handler: h.HandleMemoryStream,
			publish: obs.MemorySSE.Publish,
			msg:     `data: {"agent":"coder","repo":"owner_repo"}` + "\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cap := newSSECapture()
			req := httptest.NewRequest(http.MethodGet, "/"+tc.name, nil).WithContext(ctx)

			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.handler(cap, req)
			}()

			heartbeat := mustReadSSEMsg(t, cap.writes, 2*time.Second)
			if heartbeat != ": connected\n\n" {
				t.Fatalf("want heartbeat %q, got %q", ": connected\n\n", heartbeat)
			}

			if got := cap.Header().Get("Content-Type"); got != "text/event-stream" {
				t.Errorf("Content-Type: want %q, got %q", "text/event-stream", got)
			}
			if got := cap.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control: want %q, got %q", "no-cache", got)
			}

			tc.publish([]byte(tc.msg))
			got := mustReadSSEMsg(t, cap.writes, 2*time.Second)
			if got != tc.msg {
				t.Errorf("published %q but received %q", tc.msg, got)
			}

			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("handler did not exit after context cancellation")
			}
		})
	}
}

// ── /traces ────────────────────────────────────────────────────────────────

func TestHandleTracesReturnsStoredSpans(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	now := time.Now().UTC()
	obs.RecordSpan(workflow.SpanInput{SpanID: "s1", RootEventID: "root-A", Agent: "coder", Backend: "claude", Repo: "owner/repo", EventKind: "issues.labeled", Number: 1, StartedAt: now, FinishedAt: now.Add(5 * time.Second), Status: "success"})
	obs.RecordSpan(workflow.SpanInput{SpanID: "s2", RootEventID: "root-A", Agent: "reviewer", Backend: "claude", Repo: "owner/repo", EventKind: "agent.dispatch", InvokedBy: "coder", Number: 1, DispatchDepth: 1, StartedAt: now.Add(time.Second), FinishedAt: now.Add(6 * time.Second), Status: "success"})
	time.Sleep(50 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/traces", nil)
	rec := httptest.NewRecorder()
	h.HandleTraces(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var spans []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&spans); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
}

func TestHandleTraceByRootEventID(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	now := time.Now().UTC()
	obs.RecordSpan(workflow.SpanInput{SpanID: "s1", RootEventID: "root-A", Agent: "coder", Backend: "claude", Repo: "r", EventKind: "issues.labeled", Number: 1, StartedAt: now, FinishedAt: now.Add(time.Second), Status: "success"})
	obs.RecordSpan(workflow.SpanInput{SpanID: "s2", RootEventID: "root-B", Agent: "reviewer", Backend: "claude", Repo: "r", EventKind: "push", StartedAt: now, FinishedAt: now.Add(time.Second), Status: "success"})
	time.Sleep(50 * time.Millisecond)

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/traces/root-A", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var spans []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&spans)
	if len(spans) != 1 {
		t.Fatalf("want 1 span for root-A, got %d", len(spans))
	}
}

func TestHandleTraceNotFound(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/traces/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// ── /graph ─────────────────────────────────────────────────────────────────

func TestHandleGraphReturnsEdges(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	obs.RecordDispatch("coder", "reviewer", "owner/repo", 10, "needs review")
	obs.RecordDispatch("coder", "reviewer", "owner/repo", 11, "follow-up")
	time.Sleep(50 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	h.HandleGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g graphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(g.Edges))
	}
	if g.Edges[0].Count != 2 {
		t.Fatalf("want edge count=2, got %d", g.Edges[0].Count)
	}
}

func TestHandleGraphEmptyWhenNoDispatches(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	h.HandleGraph(rec, req)

	var g graphJSON
	_ = json.NewDecoder(rec.Body).Decode(&g)
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("want empty graph, got %+v", g)
	}
}

func TestHandleGraphIncludesConfiguredAgentWithNoDispatches(t *testing.T) {
	t.Parallel()
	cfg := minimalCfg()
	cfg.Agents = []fleet.Agent{{Name: "solo-agent", Backend: "claude", Prompt: "p"}}
	fx := newFixture(t, cfg)
	h := New(fx.events, fx.store, nil, nil, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	h.HandleGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g graphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("want 1 node for configured agent, got %d", len(g.Nodes))
	}
	if g.Nodes[0].ID != "solo-agent" {
		t.Fatalf("want node ID %q, got %q", "solo-agent", g.Nodes[0].ID)
	}
	if len(g.Edges) != 0 {
		t.Fatalf("want 0 edges, got %d", len(g.Edges))
	}
}

func TestHandleGraphNodeStatusReflectsRuntimeState(t *testing.T) {
	t.Parallel()
	cfg := minimalCfg()
	cfg.Agents = []fleet.Agent{
		{Name: "runner", Backend: "claude", Prompt: "p"},
		{Name: "idle-ok", Backend: "claude", Prompt: "p"},
		{Name: "idle-err", Backend: "claude", Prompt: "p"},
	}
	fx := newFixture(t, cfg)
	// Mark "runner" as in-flight via the same hook the engine uses.
	fx.events.ActiveRuns.StartRun("runner")
	// Seed the scheduler so "idle-err" has a recorded last_status="error".
	sched := newSchedulerWithStatuses(t, []scheduler.AgentStatus{
		{Name: "idle-err", Repo: "owner/r", LastStatus: "error"},
	})
	h := New(fx.events, fx.store, sched, nil, nil, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	rec := httptest.NewRecorder()
	h.HandleGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var g graphJSON
	if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	statusByID := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		statusByID[n.ID] = n.Status
	}
	if statusByID["runner"] != "running" {
		t.Errorf("running agent: want status=%q, got %q", "running", statusByID["runner"])
	}
	if statusByID["idle-err"] != "error" {
		t.Errorf("error agent: want status=%q, got %q", "error", statusByID["idle-err"])
	}
	if statusByID["idle-ok"] != "" {
		t.Errorf("idle-ok agent: want empty status, got %q", statusByID["idle-ok"])
	}
}

// ── SSE helper ─────────────────────────────────────────────────────────────

// TestServeSSEHeartbeatSentPeriodically verifies that ServeSSEWithInterval
// writes periodic ": heartbeat" SSE comments when no data arrives.
func TestServeSSEHeartbeatSentPeriodically(t *testing.T) {
	t.Parallel()

	hub := obstore.NewSSEHub(4)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeSSEWithInterval(w, r, hub, 20*time.Millisecond)
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: want text/event-stream, got %q", ct)
	}

	deadline := time.After(500 * time.Millisecond)
	scanner := bufio.NewScanner(resp.Body)
	var seenConnected, seenHeartbeat bool
	lineCh := make(chan string)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()
	for !seenConnected || !seenHeartbeat {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatal("SSE stream closed before heartbeat was received")
			}
			switch line {
			case ": connected":
				seenConnected = true
			case ": heartbeat":
				seenHeartbeat = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for SSE heartbeat (connected=%v heartbeat=%v)", seenConnected, seenHeartbeat)
		}
	}
}

// TestServeSSEDeliversDataMessages verifies messages published to the hub
// are forwarded as "data: ...\n\n" frames.
func TestServeSSEDeliversDataMessages(t *testing.T) {
	t.Parallel()

	hub := obstore.NewSSEHub(4)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeSSEWithInterval(w, r, hub, time.Hour) // suppress heartbeat during this test
	}))
	defer ts.Close()

	resp, err := http.Get(ts.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	lineCh := make(chan string)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()
	select {
	case line := <-lineCh:
		if line != ": connected" {
			t.Fatalf("expected ': connected', got %q", line)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for initial connected comment")
	}

	payload := []byte("data: hello\n\n")
	hub.Publish(payload)

	deadline := time.After(500 * time.Millisecond)
	var received []string
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("stream closed before message was received; lines so far: %v", received)
			}
			received = append(received, line)
			if strings.Contains(strings.Join(received, "\n"), "data: hello") {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for data message; lines so far: %v", received)
		}
	}
}

// ── /memory ────────────────────────────────────────────────────────────────

func TestHandleMemorySQLiteMode(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		agent     string
		repo      string
		stored    map[string]string
		mtimes    map[string]time.Time
		wantCode  int
		wantBody  string
		wantMtime string
	}{
		{
			name:     "returns stored memory",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{"coder\x00owner_repo": "# memory"},
			wantCode: http.StatusOK,
			wantBody: "# memory",
		},
		{
			name:     "missing record returns 404",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "existing empty memory returns 200",
			agent:    "coder",
			repo:     "owner_repo",
			stored:   map[string]string{"coder\x00owner_repo": ""},
			wantCode: http.StatusOK,
			wantBody: "",
		},
		{
			name:      "X-Memory-Mtime set from SQLite updated_at",
			agent:     "coder",
			repo:      "owner_repo",
			stored:    map[string]string{"coder\x00owner_repo": "# memory"},
			mtimes:    map[string]time.Time{"coder\x00owner_repo": fixedTime},
			wantCode:  http.StatusOK,
			wantBody:  "# memory",
			wantMtime: fixedTime.UTC().Format(time.RFC3339),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, nil)
			memReader := seedMemoryReader(t, fx.db, tc.stored, tc.mtimes)
			h := New(fx.events, fx.store, nil, nil, memReader, zerolog.Nop())

			router := newRouter(h)
			req := httptest.NewRequest(http.MethodGet, "/memory/"+tc.agent+"/"+tc.repo, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("want body %q, got %q", tc.wantBody, got)
				}
			}
			// Only assert the X-Memory-Mtime header when the test pinned an
			// expected value. The real SQLite store auto-stamps updated_at
			// on every write, so a "want empty" assertion no longer makes
			// sense — mtime is always populated for present rows.
			if tc.wantMtime != "" {
				if got := rec.Header().Get("X-Memory-Mtime"); got != tc.wantMtime {
					t.Fatalf("X-Memory-Mtime: want %q, got %q", tc.wantMtime, got)
				}
			} else if rec.Code == http.StatusOK {
				if got := rec.Header().Get("X-Memory-Mtime"); got == "" {
					t.Fatalf("X-Memory-Mtime: want non-empty for present row, got empty")
				}
			}
		})
	}
}

func TestHandleMemoryReturns503WhenReaderUnconfigured(t *testing.T) {
	t.Parallel()
	h := newPlainHandler(t) // no memReader

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/memory/coder/owner_repo", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when no reader configured, got %d", rec.Code)
	}
}

// ── /traces/{span_id}/steps ────────────────────────────────────────────────

func TestHandleTraceStepsReturnsOrderedSteps(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	steps := []workflow.TraceStep{
		{ToolName: "Bash", InputSummary: "go test", OutputSummary: "ok", DurationMs: 300},
		{ToolName: "Read", InputSummary: "/main.go", OutputSummary: "package main", DurationMs: 10},
	}
	obs.RecordSteps("span-abc", steps)

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/traces/span-abc/steps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got []workflow.TraceStep
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 steps, got %d", len(got))
	}
	if got[0].ToolName != "Bash" || got[1].ToolName != "Read" {
		t.Fatalf("unexpected order: %v %v", got[0].ToolName, got[1].ToolName)
	}
	if got[0].DurationMs != 300 {
		t.Fatalf("want DurationMs=300 for first step, got %d", got[0].DurationMs)
	}
}

func TestHandleTraceStepsEmptyArray(t *testing.T) {
	t.Parallel()
	obs := newTestEvents(t)
	h := newHandlerOnStore(t, obs)

	router := newRouter(h)
	req := httptest.NewRequest(http.MethodGet, "/traces/no-such-span/steps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got []workflow.TraceStep
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil {
		t.Fatal("want empty JSON array, got null")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 steps, got %d", len(got))
	}
}
