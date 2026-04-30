package observe_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// testDB opens a temporary SQLite database for testing.
func testDB(t *testing.T) *observe.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return observe.NewStore(db)
}

// ─── SSEHub tests ──────────────────────────────────────────────────────────────

func TestSSEHubPublishDelivers(t *testing.T) {
	t.Parallel()
	h := observe.NewSSEHub(4)
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	h.Publish([]byte("hello"))

	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Fatalf("want 'hello', got %q", string(msg))
		}
	default:
		t.Fatal("no message received")
	}
}

func TestSSEHubSlowSubscriberDropsOldest(t *testing.T) {
	t.Parallel()
	h := observe.NewSSEHub(1) // tiny buffer
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	h.Publish([]byte("first"))  // fills the buffer
	h.Publish([]byte("second")) // should drop "first" and place "second"

	msg := <-ch
	if string(msg) != "second" {
		t.Fatalf("want 'second' (oldest dropped), got %q", string(msg))
	}
}

// TestSSEHubPublishUnsubscribeRace verifies that concurrent Publish and
// Unsubscribe calls do not panic. Previously Unsubscribe closed the channel
// while a concurrent Publish (which had already snapshotted the channel list)
// could still send to it, causing a send-on-closed-channel panic.
func TestSSEHubPublishUnsubscribeRace(t *testing.T) {
	t.Parallel()
	// Use a large buffer so Publish can write even after the reader is gone.
	h := observe.NewSSEHub(64)

	const goroutines = 50
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch := h.Subscribe()
			// Race: publish (which snapshots the list) concurrently with unsubscribe.
			h.Publish([]byte("msg"))
			h.Unsubscribe(ch)
		}()
		go func() {
			defer wg.Done()
			h.Publish([]byte("concurrent"))
		}()
	}
	wg.Wait()
}

// ─── WatchMemoryDir tests ──────────────────────────────────────────────────

func TestWatchMemoryDirPublishesOnChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentDir := filepath.Join(dir, "coder")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(agentDir, "owner_repo.md")
	if err := os.WriteFile(filePath, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	hub := observe.NewSSEHub(8)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a very short interval so the test finishes quickly.
	go observe.WatchMemoryDir(ctx, dir, 25*time.Millisecond, hub)

	// Wait for the baseline scan to complete (one interval).
	time.Sleep(60 * time.Millisecond)

	// Advance mtime by writing again. On modern filesystems (tmpfs, ext4) the
	// new mtime will differ from the initial write even within the same second.
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Expect a change notification within a reasonable deadline.
	select {
	case raw := <-ch:
		var ev observe.MemoryChangeEvent
		if err := json.Unmarshal(extractSSEData(raw), &ev); err != nil {
			t.Fatalf("could not unmarshal SSE payload: %v (raw: %s)", err, raw)
		}
		if ev.Agent != "coder" {
			t.Errorf("agent: want %q, got %q", "coder", ev.Agent)
		}
		if ev.Repo != "owner_repo" {
			t.Errorf("repo: want %q, got %q", "owner_repo", ev.Repo)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for memory change SSE event")
	}
}

func TestWatchMemoryDirNoPublishOnFirstScan(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentDir := filepath.Join(dir, "coder")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "repo.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	hub := observe.NewSSEHub(8)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go observe.WatchMemoryDir(ctx, dir, 25*time.Millisecond, hub)

	// After two full scan cycles the initial file must not have produced an event.
	time.Sleep(80 * time.Millisecond)

	select {
	case msg := <-ch:
		t.Fatalf("unexpected SSE event on first scan: %s", msg)
	default:
	}
}

// TestWatchMemoryDirPublishesOnNewFileAfterBaseline verifies that a markdown
// file created after the watcher has completed its baseline scan triggers a
// MemoryChangeEvent, even though the file did not exist at startup.
func TestWatchMemoryDirPublishesOnNewFileAfterBaseline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentDir := filepath.Join(dir, "coder")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hub := observe.NewSSEHub(8)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go observe.WatchMemoryDir(ctx, dir, 25*time.Millisecond, hub)

	// Wait for the baseline scan to complete before creating the new file.
	time.Sleep(60 * time.Millisecond)

	newFile := filepath.Join(agentDir, "new_repo.md")
	if err := os.WriteFile(newFile, []byte("first write"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Expect a change notification for the newly created file.
	select {
	case raw := <-ch:
		var ev observe.MemoryChangeEvent
		if err := json.Unmarshal(extractSSEData(raw), &ev); err != nil {
			t.Fatalf("could not unmarshal SSE payload: %v (raw: %s)", err, raw)
		}
		if ev.Agent != "coder" {
			t.Errorf("agent: want %q, got %q", "coder", ev.Agent)
		}
		if ev.Repo != "new_repo" {
			t.Errorf("repo: want %q, got %q", "new_repo", ev.Repo)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for new-file MemoryChangeEvent")
	}
}

// TestWatchMemoryDirNoOpOnEmptyDir verifies that WatchMemoryDir exits
// immediately and publishes no events when dir is empty.
func TestWatchMemoryDirNoOpOnEmptyDir(t *testing.T) {
	t.Parallel()

	hub := observe.NewSSEHub(8)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go observe.WatchMemoryDir(ctx, "", 25*time.Millisecond, hub)

	// Allow several tick cycles to elapse; the watcher must not publish anything.
	time.Sleep(80 * time.Millisecond)

	select {
	case msg := <-ch:
		t.Fatalf("unexpected SSE event from empty-dir watcher: %s", msg)
	default:
	}
}

// extractSSEData strips the "data: " prefix and trailing newlines added by
// sseData so the payload can be unmarshalled as JSON.
func extractSSEData(raw []byte) []byte {
	s, _ := strings.CutPrefix(string(raw), "data: ")
	return []byte(strings.TrimRight(s, "\n"))
}

func TestExtractSSEData(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"normal SSE frame", []byte("data: {\"k\":\"v\"}\n\n"), []byte("{\"k\":\"v\"}")},
		{"prefix-only returns empty", []byte("data: "), []byte("")},
		{"no prefix unchanged", []byte("something else"), []byte("something else")},
		{"trailing newlines stripped", []byte("data: payload\n\n"), []byte("payload")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractSSEData(tc.in)
			if string(got) != string(tc.want) {
				t.Errorf("extractSSEData(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ─── ActiveRuns ──────────────────────────────────────────────────────────────

func TestRunRegistryListActiveAndStream(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	now := time.Now()
	s.Runs.BeginRun(observe.ActiveRun{
		SpanID: "sp-A", EventID: "ev-1", Agent: "coder", Backend: "claude",
		Repo: "owner/r", EventKind: "issues.labeled", StartedAt: now,
	})
	s.Runs.BeginRun(observe.ActiveRun{
		SpanID: "sp-B", EventID: "ev-1", Agent: "reviewer", Backend: "claude",
		Repo: "owner/r", EventKind: "issues.labeled", StartedAt: now,
	})

	active := s.Runs.ListActive("ev-1")
	if len(active) != 2 {
		t.Fatalf("active = %d, want 2", len(active))
	}

	// Subscribe → publish → see line. Use a buffered fixture by
	// publishing a line BEFORE subscribing, which exercises the
	// history-replay path.
	s.Runs.PublishLine("sp-A", []byte("line-pre-sub"))
	hist, ch, ok := s.Runs.SubscribeStream("sp-A")
	if !ok {
		t.Fatal("expected stream for sp-A")
	}
	defer s.Runs.UnsubscribeStream("sp-A", ch)
	if len(hist) != 1 || string(hist[0]) != "line-pre-sub" {
		t.Errorf("history = %v, want [line-pre-sub]", hist)
	}

	// Live publish after subscribe lands on the channel.
	s.Runs.PublishLine("sp-A", []byte("line-live"))
	select {
	case got := <-ch:
		if string(got) != "line-live" {
			t.Errorf("live line = %q, want line-live", got)
		}
	case <-time.After(time.Second):
		t.Fatal("live channel did not receive the published line")
	}

	// EndRun removes the active entry and closes the channel.
	s.Runs.EndRun("sp-A")
	if got := s.Runs.ListActive("ev-1"); len(got) != 1 {
		t.Errorf("active after EndRun(sp-A) = %d, want 1", len(got))
	}
	select {
	case _, open := <-ch:
		if open {
			t.Error("expected channel to be closed after EndRun")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close after EndRun")
	}

	// Subscribing after EndRun: still works during the grace window,
	// returns history with a closed channel so the SSE handler exits
	// cleanly after replay.
	hist2, ch2, ok2 := s.Runs.SubscribeStream("sp-A")
	if !ok2 {
		t.Fatal("expected post-end subscribe to succeed during grace window")
	}
	if len(hist2) < 2 {
		t.Errorf("post-end history len = %d, want >= 2", len(hist2))
	}
	if _, open := <-ch2; open {
		t.Error("post-end channel should be closed, was open")
	}

	// Unknown span returns ok=false.
	if _, _, ok := s.Runs.SubscribeStream("nope"); ok {
		t.Error("expected ok=false for unknown span")
	}
}

func TestActiveRunsStartFinishIsRunning(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	ar := s.ActiveRuns

	if ar.IsRunning("coder") {
		t.Fatal("want not running before Start")
	}

	ar.StartRun("coder")
	if !ar.IsRunning("coder") {
		t.Fatal("want running after StartRun")
	}
	if ar.IsRunning("reviewer") {
		t.Fatal("want reviewer not running")
	}

	ar.FinishRun("coder")
	if ar.IsRunning("coder") {
		t.Fatal("want not running after FinishRun")
	}
}

func TestActiveRunsConcurrentRuns(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	ar := s.ActiveRuns

	// Two concurrent runs for the same agent.
	ar.StartRun("coder")
	ar.StartRun("coder")
	if !ar.IsRunning("coder") {
		t.Fatal("want running with two active runs")
	}

	ar.FinishRun("coder")
	if !ar.IsRunning("coder") {
		t.Fatal("want still running after first finish (second run still active)")
	}

	ar.FinishRun("coder")
	if ar.IsRunning("coder") {
		t.Fatal("want not running after both runs finish")
	}
}

func TestActiveRunsFinishBelowZeroIsSafe(t *testing.T) {
	t.Parallel()
	s := testDB(t)
	ar := s.ActiveRuns

	// Calling FinishRun without a matching Start must not panic or go negative.
	ar.FinishRun("ghost")
	if ar.IsRunning("ghost") {
		t.Fatal("want not running after spurious FinishRun")
	}
}

func TestStoreIsRunningDelegates(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	s.ActiveRuns.StartRun("coder")
	if !s.IsRunning("coder") {
		t.Fatal("Store.IsRunning must delegate to ActiveRuns")
	}
	s.ActiveRuns.FinishRun("coder")
	if s.IsRunning("coder") {
		t.Fatal("Store.IsRunning must return false after run finishes")
	}
}

// ─── Store.RecordEvent ────────────────────────────────────────────────────────

// TestStoreRecordEventPersistsAndPublishesToSSE verifies that RecordEvent
// both persists the event to SQLite and fans it out to EventsSSE.
func TestStoreRecordEventPersistsAndPublishesToSSE(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	ch := s.EventsSSE.Subscribe()
	defer s.EventsSSE.Unsubscribe(ch)

	at := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	ev := workflow.Event{
		ID:     "delivery-1",
		Repo:   workflow.RepoRef{FullName: "owner/repo"},
		Kind:   "issues.labeled",
		Number: 42,
		Actor:  "alice",
	}
	s.RecordEvent(at, ev)

	// Wait briefly for the async goroutine to persist.
	time.Sleep(50 * time.Millisecond)

	// Verify SQLite received the event via ListEvents.
	stored := s.ListEvents(time.Time{})
	if len(stored) != 1 {
		t.Fatalf("want 1 event in DB, got %d", len(stored))
	}
	got := stored[0]
	if got.ID != "delivery-1" {
		t.Errorf("ID = %q, want %q", got.ID, "delivery-1")
	}
	if got.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want %q", got.Repo, "owner/repo")
	}
	if got.Kind != "issues.labeled" {
		t.Errorf("Kind = %q, want %q", got.Kind, "issues.labeled")
	}
	if got.Number != 42 {
		t.Errorf("Number = %d, want 42", got.Number)
	}

	// Verify SSE fan-out: message should be available immediately because
	// SSEHub.Publish sends synchronously to the buffered subscriber channels.
	select {
	case msg := <-ch:
		if !strings.HasPrefix(string(msg), "data: ") {
			t.Fatalf("SSE message should start with \"data: \", got %q", msg)
		}
	default:
		t.Fatal("want SSE message, got nothing")
	}
}

// TestStoreRecordEventSSEUsesLowercaseJSON is a regression guard for the
// SSE/REST field-name mismatch: TimestampedEvent must serialize with the same
// lowercase JSON keys that apiEventJSON uses, so the dashboard EventSource
// handler can parse both streams with the same client-side Event interface.
func TestStoreRecordEventSSEUsesLowercaseJSON(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	ch := s.EventsSSE.Subscribe()
	defer s.EventsSSE.Unsubscribe(ch)

	s.RecordEvent(time.Now(), workflow.Event{
		ID:     "evt-lc",
		Repo:   workflow.RepoRef{FullName: "owner/repo"},
		Kind:   "push",
		Number: 0,
		Actor:  "bot",
	})

	var msg []byte
	select {
	case msg = <-ch:
	default:
		t.Fatal("want SSE message, got nothing")
	}

	// Strip the SSE framing and unmarshal into a raw map to inspect key names.
	var fields map[string]any
	if err := json.Unmarshal(extractSSEData(msg), &fields); err != nil {
		t.Fatalf("unmarshal SSE payload: %v", err)
	}

	for _, key := range []string{"at", "id", "repo", "kind", "number", "actor"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("SSE payload missing lowercase key %q; got keys: %v", key, mapKeys(fields))
		}
	}
	// Ensure no uppercase duplicates leaked through.
	for _, key := range []string{"At", "ID", "Repo", "Kind", "Number", "Actor"} {
		if _, ok := fields[key]; ok {
			t.Errorf("SSE payload contains unexpected uppercase key %q", key)
		}
	}
}

// ─── Store.RecordSpan ─────────────────────────────────────────────────────────

// TestStoreRecordSpanPersistsAndPublishesToSSE verifies that RecordSpan
// both stores the span in SQLite and fans it out to TracesSSE.
func TestStoreRecordSpanPersistsAndPublishesToSSE(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	ch := s.TracesSSE.Subscribe()
	defer s.TracesSSE.Unsubscribe(ch)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Second)
	s.RecordSpan(workflow.SpanInput{
		SpanID: "span-1", RootEventID: "root-1",
		Agent: "coder", Backend: "claude",
		Repo: "owner/repo", EventKind: "issues.labeled",
		Number: 7, QueueWaitMs: 50, ArtifactsCount: 3, Summary: "all done",
		StartedAt: start, FinishedAt: end,
		Status: "success",
	})

	// Wait for async persistence.
	time.Sleep(50 * time.Millisecond)

	// Verify SQLite via ListTraces.
	spans := s.ListTraces()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	sp := spans[0]
	if sp.SpanID != "span-1" {
		t.Errorf("SpanID = %q, want %q", sp.SpanID, "span-1")
	}
	if sp.Agent != "coder" {
		t.Errorf("Agent = %q, want %q", sp.Agent, "coder")
	}
	if sp.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want 5000", sp.DurationMs)
	}
	if sp.Status != "success" {
		t.Errorf("Status = %q, want %q", sp.Status, "success")
	}

	// Verify SSE fan-out.
	select {
	case msg := <-ch:
		if !strings.HasPrefix(string(msg), "data: ") {
			t.Fatalf("SSE message should start with \"data: \", got %q", msg)
		}
	default:
		t.Fatal("want SSE message on TracesSSE, got nothing")
	}
}

// ─── Store.RecordDispatch ─────────────────────────────────────────────────────

// TestStoreRecordDispatchPersistsToDB verifies that RecordDispatch persists
// to SQLite and the edge is visible via ListEdges().
func TestStoreRecordDispatchPersistsToDB(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	s.RecordDispatch("coder", "reviewer", "owner/repo", 42, "needs review")

	// Wait for async persistence.
	time.Sleep(50 * time.Millisecond)

	edges := s.ListEdges()
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.From != "coder" || e.To != "reviewer" {
		t.Errorf("edge = %q -> %q, want %q -> %q", e.From, e.To, "coder", "reviewer")
	}
	if e.Count != 1 {
		t.Errorf("Count = %d, want 1", e.Count)
	}
	if len(e.Dispatches) != 1 {
		t.Fatalf("want 1 dispatch record, got %d", len(e.Dispatches))
	}
	d := e.Dispatches[0]
	if d.Repo != "owner/repo" || d.Number != 42 || d.Reason != "needs review" {
		t.Errorf("dispatch record = %+v, unexpected", d)
	}
}

// ─── Store.ListEvents since filter ───────────────────────────────────────────

func TestStoreListEventsSinceFilter(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	s.RecordEvent(base, workflow.Event{ID: "old", Kind: "push"})
	s.RecordEvent(base.Add(2*time.Second), workflow.Event{ID: "new", Kind: "push"})

	// Wait for async persistence.
	time.Sleep(50 * time.Millisecond)

	events := s.ListEvents(base.Add(time.Second))
	if len(events) != 1 {
		t.Fatalf("want 1 event after filter, got %d", len(events))
	}
	if events[0].ID != "new" {
		t.Fatalf("want 'new' event, got %q", events[0].ID)
	}
}

// ─── Store.TracesByRootEventID ───────────────────────────────────────────────

func TestStoreTracesByRootEventID(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	s.RecordSpan(workflow.SpanInput{SpanID: "s1", RootEventID: "root-A", Agent: "coder", Backend: "claude", Repo: "r", EventKind: "issues.labeled", Number: 1, StartedAt: now, FinishedAt: now.Add(time.Second), Status: "success"})
	s.RecordSpan(workflow.SpanInput{SpanID: "s2", RootEventID: "root-B", Agent: "reviewer", Backend: "claude", Repo: "r", EventKind: "push", StartedAt: now, FinishedAt: now.Add(time.Second), Status: "success"})
	s.RecordSpan(workflow.SpanInput{SpanID: "s3", RootEventID: "root-A", Agent: "coder", Backend: "claude", Repo: "r", EventKind: "agent.dispatch", Number: 1, DispatchDepth: 1, StartedAt: now.Add(time.Second), FinishedAt: now.Add(2 * time.Second), Status: "success"})

	// Poll until both spans for root-A are persisted (RecordSpan is async).
	deadline := time.Now().Add(500 * time.Millisecond)
	var rootA []observe.Span
	for time.Now().Before(deadline) {
		rootA = s.TracesByRootEventID("root-A")
		if len(rootA) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rootA) != 2 {
		t.Fatalf("want 2 spans for root-A, got %d", len(rootA))
	}
	rootB := s.TracesByRootEventID("root-B")
	if len(rootB) != 1 {
		t.Fatalf("want 1 span for root-B, got %d", len(rootB))
	}
}

// ─── Store.RecordSteps / ListSteps ───────────────────────────────────────────

func TestStoreRecordAndListSteps(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	steps := []workflow.TraceStep{
		{ToolName: "Bash", InputSummary: "go test ./...", OutputSummary: "ok", DurationMs: 200},
		{ToolName: "Read", InputSummary: "/foo.go", OutputSummary: "package foo", DurationMs: 50},
	}
	s.RecordSteps("span-1", steps)

	got := s.ListSteps("span-1")
	if len(got) != 2 {
		t.Fatalf("want 2 steps, got %d", len(got))
	}
	if got[0].ToolName != "Bash" || got[1].ToolName != "Read" {
		t.Fatalf("unexpected order: %v %v", got[0].ToolName, got[1].ToolName)
	}
	if got[0].DurationMs != 200 || got[1].DurationMs != 50 {
		t.Fatalf("unexpected DurationMs: %d %d", got[0].DurationMs, got[1].DurationMs)
	}
}

func TestStoreListStepsEmptyWhenNoneRecorded(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	got := s.ListSteps("no-such-span")
	if got != nil {
		t.Fatalf("want nil for unknown span, got %v", got)
	}
}

func TestStoreRecordStepsNoOpOnEmpty(t *testing.T) {
	t.Parallel()
	s := testDB(t)

	s.RecordSteps("span-x", nil)
	s.RecordSteps("span-x", []workflow.TraceStep{})

	got := s.ListSteps("span-x")
	if got != nil {
		t.Fatalf("want nil after no-op records, got %v", got)
	}
}

// mapKeys returns the sorted keys of a map for use in error messages.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
