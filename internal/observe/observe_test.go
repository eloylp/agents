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

// extractSSEData strips the "data: " prefix and trailing "\n\n" added by
// sseData so the payload can be unmarshalled as JSON.
func extractSSEData(raw []byte) []byte {
	const prefix = "data: "
	s := string(raw)
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		s = s[len(prefix):]
	}
	s = strings.TrimRight(s, "\n")
	return []byte(s)
}
