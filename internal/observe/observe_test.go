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
	"github.com/eloylp/agents/internal/workflow"
)

// ─── EventBuffer tests ────────────────────────────────────────────────────────

func TestEventBufferListOrdering(t *testing.T) {
	t.Parallel()
	b := observe.NewEventBuffer(5)
	now := time.Now()
	for i := range 3 {
		b.Add(observe.TimestampedEvent{At: now.Add(time.Duration(i) * time.Second), ID: string(rune('A' + i))})
	}
	events := b.List(time.Time{})
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if events[0].ID != "A" || events[1].ID != "B" || events[2].ID != "C" {
		t.Fatalf("unexpected order: %v", events)
	}
}

func TestEventBufferRingOverwrite(t *testing.T) {
	t.Parallel()
	b := observe.NewEventBuffer(3)
	now := time.Now()
	for i := range 5 {
		b.Add(observe.TimestampedEvent{At: now.Add(time.Duration(i) * time.Second), ID: string(rune('A' + i))})
	}
	events := b.List(time.Time{})
	if len(events) != 3 {
		t.Fatalf("want 3 events (ring full), got %d", len(events))
	}
	// The oldest 2 (A, B) must have been overwritten; C, D, E remain.
	if events[0].ID != "C" || events[1].ID != "D" || events[2].ID != "E" {
		t.Fatalf("unexpected ring content: %v", events)
	}
}

func TestEventBufferSinceFilter(t *testing.T) {
	t.Parallel()
	b := observe.NewEventBuffer(10)
	base := time.Now()
	for i := range 5 {
		b.Add(observe.TimestampedEvent{At: base.Add(time.Duration(i) * time.Second), ID: string(rune('A' + i))})
	}
	// events[2] is at base+2s; since=base+2s should exclude it (not strictly after)
	events := b.List(base.Add(2 * time.Second))
	if len(events) != 2 {
		t.Fatalf("want 2 events after filter, got %d", len(events))
	}
	if events[0].ID != "D" || events[1].ID != "E" {
		t.Fatalf("unexpected filtered content: %v", events)
	}
}

// ─── TraceBuffer tests ────────────────────────────────────────────────────────

func TestTraceBufferListAndByRootEventID(t *testing.T) {
	t.Parallel()
	b := observe.NewTraceBuffer(10)
	b.Add(observe.Span{SpanID: "s1", RootEventID: "root-A", Agent: "coder"})
	b.Add(observe.Span{SpanID: "s2", RootEventID: "root-B", Agent: "reviewer"})
	b.Add(observe.Span{SpanID: "s3", RootEventID: "root-A", Agent: "coder"})

	all := b.List()
	if len(all) != 3 {
		t.Fatalf("want 3 spans, got %d", len(all))
	}

	rootA := b.ByRootEventID("root-A")
	if len(rootA) != 2 {
		t.Fatalf("want 2 spans for root-A, got %d", len(rootA))
	}

	rootB := b.ByRootEventID("root-B")
	if len(rootB) != 1 {
		t.Fatalf("want 1 span for root-B, got %d", len(rootB))
	}
}

func TestTraceBufferRingOverwrite(t *testing.T) {
	t.Parallel()
	b := observe.NewTraceBuffer(2)
	b.Add(observe.Span{SpanID: "s1"})
	b.Add(observe.Span{SpanID: "s2"})
	b.Add(observe.Span{SpanID: "s3"})

	all := b.List()
	if len(all) != 2 {
		t.Fatalf("want 2 (ring capacity), got %d", len(all))
	}
	if all[0].SpanID != "s2" || all[1].SpanID != "s3" {
		t.Fatalf("unexpected spans: %v", all)
	}
}

// ─── InteractionGraph tests ────────────────────────────────────────────────────

func TestInteractionGraphRecord(t *testing.T) {
	t.Parallel()
	g := observe.NewInteractionGraph(5)
	g.Record("coder", "reviewer", "owner/repo", 1, "review needed")
	g.Record("coder", "reviewer", "owner/repo", 2, "follow-up")

	edges := g.Edges()
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.From != "coder" || e.To != "reviewer" {
		t.Fatalf("unexpected edge: %+v", e)
	}
	if e.Count != 2 {
		t.Fatalf("want count 2, got %d", e.Count)
	}
	if len(e.Dispatches) != 2 {
		t.Fatalf("want 2 dispatch records, got %d", len(e.Dispatches))
	}
}

func TestInteractionGraphDispatchLimit(t *testing.T) {
	t.Parallel()
	g := observe.NewInteractionGraph(3)
	for i := range 5 {
		g.Record("A", "B", "r", i, "r")
	}
	edges := g.Edges()
	if len(edges[0].Dispatches) != 3 {
		t.Fatalf("want 3 dispatches (limit), got %d", len(edges[0].Dispatches))
	}
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

func TestActiveRunsStartFinishIsRunning(t *testing.T) {
	t.Parallel()
	s := observe.NewStore()
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
	s := observe.NewStore()
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
	s := observe.NewStore()
	ar := s.ActiveRuns

	// Calling FinishRun without a matching Start must not panic or go negative.
	ar.FinishRun("ghost")
	if ar.IsRunning("ghost") {
		t.Fatal("want not running after spurious FinishRun")
	}
}

func TestStoreIsRunningDelegates(t *testing.T) {
	t.Parallel()
	s := observe.NewStore()

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

// TestStoreRecordEventAddsToBufferAndPublishesToSSE verifies that RecordEvent
// both persists the event in the ring buffer and fans it out to EventsSSE.
func TestStoreRecordEventAddsToBufferAndPublishesToSSE(t *testing.T) {
	t.Parallel()
	s := observe.NewStore()

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

	// Verify ring buffer received the event.
	stored := s.Events.List(time.Time{})
	if len(stored) != 1 {
		t.Fatalf("want 1 event in buffer, got %d", len(stored))
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
	if !got.At.Equal(at) {
		t.Errorf("At = %v, want %v", got.At, at)
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
	s := observe.NewStore()

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

// TestStoreRecordSpanAddsToBufferAndPublishesToSSE verifies that RecordSpan
// both stores the span in the trace ring buffer and fans it out to TracesSSE.
func TestStoreRecordSpanAddsToBufferAndPublishesToSSE(t *testing.T) {
	t.Parallel()
	s := observe.NewStore()

	ch := s.TracesSSE.Subscribe()
	defer s.TracesSSE.Unsubscribe(ch)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Second)
	s.RecordSpan(
		"span-1", "root-1", "",
		"coder", "claude",
		"owner/repo", "issues.labeled", "",
		7, 0,
		50, 3, "all done",
		start, end,
		"success", "",
	)

	// Verify ring buffer.
	spans := s.Traces.List()
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

// TestStoreRecordDispatchRecordsInGraph verifies that RecordDispatch delegates
// to the InteractionGraph and the edge is visible via Graph.Edges().
func TestStoreRecordDispatchRecordsInGraph(t *testing.T) {
	t.Parallel()
	s := observe.NewStore()

	s.RecordDispatch("coder", "reviewer", "owner/repo", 42, "needs review")

	edges := s.Graph.Edges()
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.From != "coder" || e.To != "reviewer" {
		t.Errorf("edge = %q → %q, want %q → %q", e.From, e.To, "coder", "reviewer")
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

// mapKeys returns the sorted keys of a map for use in error messages.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
