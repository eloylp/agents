package observe_test

import (
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
