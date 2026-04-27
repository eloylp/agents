package observe

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	obstore "github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/server"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// ── helpers ────────────────────────────────────────────────────────────────

// stubConfig implements ConfigGetter for tests.
type stubConfig struct{ cfg *config.Config }

func (s *stubConfig) Config() *config.Config { return s.cfg }

type stubStatusProvider struct {
	statuses []server.AgentStatus
}

func (p *stubStatusProvider) AgentStatuses() []server.AgentStatus { return p.statuses }

type stubDispatchProvider struct {
	stats workflow.DispatchStats
}

func (p *stubDispatchProvider) DispatchStats() workflow.DispatchStats { return p.stats }

type stubRuntimeState struct{ running map[string]bool }

func (s *stubRuntimeState) IsRunning(name string) bool { return s.running[name] }

// stubMemoryReader is a MemoryReader returning fixed (agent, repo) → content.
// Keys present but mapping to "" represent existing empty-memory records;
// absent keys return ErrMemoryNotFound.
type stubMemoryReader struct {
	content map[string]string
	mtimes  map[string]time.Time
}

func (r *stubMemoryReader) ReadMemory(agent, repo string) (string, time.Time, error) {
	key := agent + "\x00" + repo
	content, ok := r.content[key]
	if !ok {
		return "", time.Time{}, server.ErrMemoryNotFound
	}
	var mtime time.Time
	if r.mtimes != nil {
		mtime = r.mtimes[key]
	}
	return content, mtime, nil
}

// newTestStore creates an observe.Store backed by a temporary SQLite DB.
func newTestStore(t *testing.T) *obstore.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return obstore.NewStore(db)
}

// emptyCfg returns a fresh empty *config.Config wrapped in a ConfigGetter for
// tests that don't care about config contents.
func emptyCfg() ConfigGetter {
	return &stubConfig{cfg: &config.Config{}}
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

func TestHandleDispatchesDelegatesToProvider(t *testing.T) {
	t.Parallel()
	want := workflow.DispatchStats{RequestedTotal: 5, Enqueued: 3, Deduped: 2}
	h := New(newTestStore(t), emptyCfg(), nil, nil, &stubDispatchProvider{stats: want}, nil)

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
	if got != want {
		t.Errorf("stats: want %+v, got %+v", want, got)
	}
}

func TestHandleDispatchesZeroWhenNoProvider(t *testing.T) {
	t.Parallel()
	h := New(newTestStore(t), emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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

	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

	now := time.Now().UTC()
	obs.RecordSpan("s1", "root-A", "", "coder", "claude", "owner/repo", "issues.labeled", "", 1, 0, 0, 0, "", now, now.Add(5*time.Second), "success", "")
	obs.RecordSpan("s2", "root-A", "", "reviewer", "claude", "owner/repo", "agent.dispatch", "coder", 1, 1, 0, 0, "", now.Add(time.Second), now.Add(6*time.Second), "success", "")
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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

	now := time.Now().UTC()
	obs.RecordSpan("s1", "root-A", "", "coder", "claude", "r", "issues.labeled", "", 1, 0, 0, 0, "", now, now.Add(time.Second), "success", "")
	obs.RecordSpan("s2", "root-B", "", "reviewer", "claude", "r", "push", "", 0, 0, 0, 0, "", now, now.Add(time.Second), "success", "")
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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	cfg := &stubConfig{cfg: &config.Config{
		Agents: []fleet.Agent{{Name: "solo-agent", Backend: "claude"}},
	}}
	h := New(obs, cfg, nil, nil, nil, nil)

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
	obs := newTestStore(t)
	cfg := &stubConfig{cfg: &config.Config{
		Agents: []fleet.Agent{
			{Name: "runner", Backend: "claude"},
			{Name: "idle-ok", Backend: "claude"},
			{Name: "idle-err", Backend: "claude"},
		},
	}}
	provider := &stubStatusProvider{statuses: []server.AgentStatus{
		{Name: "idle-err", LastStatus: "error"},
	}}
	rt := &stubRuntimeState{running: map[string]bool{"runner": true}}
	h := New(obs, cfg, provider, rt, nil, nil)

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
		{
			name:      "zero timestamp omits X-Memory-Mtime header",
			agent:     "coder",
			repo:      "owner_repo",
			stored:    map[string]string{"coder\x00owner_repo": "# memory"},
			wantCode:  http.StatusOK,
			wantBody:  "# memory",
			wantMtime: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := New(newTestStore(t), emptyCfg(), nil, nil, nil, &stubMemoryReader{content: tc.stored, mtimes: tc.mtimes})

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
			if got := rec.Header().Get("X-Memory-Mtime"); got != tc.wantMtime {
				t.Fatalf("X-Memory-Mtime: want %q, got %q", tc.wantMtime, got)
			}
		})
	}
}

func TestHandleMemoryReturns503WhenReaderUnconfigured(t *testing.T) {
	t.Parallel()
	h := New(newTestStore(t), emptyCfg(), nil, nil, nil, nil) // no memReader

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
	obs := newTestStore(t)
	h := New(obs, emptyCfg(), nil, nil, nil, nil)

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
