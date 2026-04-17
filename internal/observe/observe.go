// Package observe provides in-memory observability data structures for the
// agents daemon: a bounded event ring-buffer, a trace ring-buffer for agent
// run spans, an agent-interaction graph, and an SSE fan-out hub.
//
// All types are safe for concurrent use. Ring buffers drop the oldest entry
// on overflow so that new writes never block.
package observe

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/workflow"
)

// ─── EventBuffer ─────────────────────────────────────────────────────────────

// TimestampedEvent is a workflow event with an ingestion timestamp attached.
type TimestampedEvent struct {
	At      time.Time
	ID      string
	Repo    string
	Kind    string
	Number  int
	Actor   string
	Payload map[string]any
}

// EventBuffer is a thread-safe bounded ring-buffer for TimestampedEvents.
// When full it overwrites the oldest entry rather than blocking.
type EventBuffer struct {
	mu   sync.RWMutex
	buf  []TimestampedEvent
	cap  int
	head int // next write index
	size int // current number of valid entries
}

// NewEventBuffer creates an EventBuffer that holds at most cap entries.
func NewEventBuffer(cap int) *EventBuffer {
	if cap <= 0 {
		cap = 500
	}
	return &EventBuffer{buf: make([]TimestampedEvent, cap), cap: cap}
}

// Add records ev.
func (b *EventBuffer) Add(ev TimestampedEvent) {
	b.mu.Lock()
	b.buf[b.head] = ev
	b.head = (b.head + 1) % b.cap
	if b.size < b.cap {
		b.size++
	}
	b.mu.Unlock()
}

// List returns all stored events in insertion order (oldest first).
// When since is non-zero, only events strictly after that time are returned.
func (b *EventBuffer) List(since time.Time) []TimestampedEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]TimestampedEvent, 0, b.size)
	start := (b.head - b.size + b.cap) % b.cap
	for i := range b.size {
		idx := (start + i) % b.cap
		e := b.buf[idx]
		if !since.IsZero() && !e.At.After(since) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ─── TraceBuffer ─────────────────────────────────────────────────────────────

// Span records the timing and outcome of a single agent run.
type Span struct {
	SpanID        string    `json:"span_id"`
	RootEventID   string    `json:"root_event_id"`
	ParentSpanID  string    `json:"parent_span_id,omitempty"`
	Agent          string    `json:"agent"`
	Backend        string    `json:"backend"`
	Repo           string    `json:"repo"`
	Number         int       `json:"number"`
	EventKind      string    `json:"event_kind"`
	InvokedBy      string    `json:"invoked_by,omitempty"`
	DispatchDepth  int       `json:"dispatch_depth"`
	QueueWaitMs    int64     `json:"queue_wait_ms"`   // time from enqueue to run start
	ArtifactsCount int       `json:"artifacts_count"` // number of artifacts produced
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	DurationMs     int64     `json:"duration_ms"`
	Status         string    `json:"status"` // "success" | "error"
	ErrorMsg       string    `json:"error,omitempty"`
}

// TraceBuffer is a thread-safe bounded ring-buffer for Spans.
type TraceBuffer struct {
	mu   sync.RWMutex
	buf  []Span
	cap  int
	head int
	size int
}

// NewTraceBuffer creates a TraceBuffer that holds at most cap spans.
func NewTraceBuffer(cap int) *TraceBuffer {
	if cap <= 0 {
		cap = 200
	}
	return &TraceBuffer{buf: make([]Span, cap), cap: cap}
}

// Add records a finished span.
func (b *TraceBuffer) Add(s Span) {
	b.mu.Lock()
	b.buf[b.head] = s
	b.head = (b.head + 1) % b.cap
	if b.size < b.cap {
		b.size++
	}
	b.mu.Unlock()
}

// List returns all stored spans in insertion order (oldest first).
func (b *TraceBuffer) List() []Span {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.listLocked()
}

func (b *TraceBuffer) listLocked() []Span {
	out := make([]Span, 0, b.size)
	start := (b.head - b.size + b.cap) % b.cap
	for i := range b.size {
		out = append(out, b.buf[(start+i)%b.cap])
	}
	return out
}

// ByRootEventID returns all spans whose RootEventID matches id.
func (b *TraceBuffer) ByRootEventID(id string) []Span {
	b.mu.RLock()
	defer b.mu.RUnlock()
	all := b.listLocked()
	var out []Span
	for _, s := range all {
		if s.RootEventID == id {
			out = append(out, s)
		}
	}
	return out
}

// ─── InteractionGraph ────────────────────────────────────────────────────────

// DispatchRecord is one observed inter-agent dispatch.
type DispatchRecord struct {
	At     time.Time `json:"at"`
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Reason string    `json:"reason"`
}

// Edge connects two agents in the interaction graph.
type Edge struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	Count      int              `json:"count"`
	Dispatches []DispatchRecord `json:"dispatches"`
}

// InteractionGraph tracks agent-to-agent dispatches as a directed weighted graph.
type InteractionGraph struct {
	mu    sync.RWMutex
	edges map[string]*Edge // key: "from\x00to"
	limit int              // max dispatches kept per edge
}

// NewInteractionGraph creates an InteractionGraph. dispatchLimit is the maximum
// number of DispatchRecords retained per edge (oldest are dropped on overflow).
func NewInteractionGraph(dispatchLimit int) *InteractionGraph {
	if dispatchLimit <= 0 {
		dispatchLimit = 100
	}
	return &InteractionGraph{
		edges: make(map[string]*Edge),
		limit: dispatchLimit,
	}
}

// Record adds one observed dispatch from → to.
func (g *InteractionGraph) Record(from, to, repo string, number int, reason string) {
	key := from + "\x00" + to
	rec := DispatchRecord{At: time.Now().UTC(), Repo: repo, Number: number, Reason: reason}
	g.mu.Lock()
	e, ok := g.edges[key]
	if !ok {
		e = &Edge{From: from, To: to}
		g.edges[key] = e
	}
	e.Count++
	e.Dispatches = append(e.Dispatches, rec)
	if len(e.Dispatches) > g.limit {
		e.Dispatches = e.Dispatches[len(e.Dispatches)-g.limit:]
	}
	g.mu.Unlock()
}

// Edges returns a snapshot of all edges.
func (g *InteractionGraph) Edges() []Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Edge, 0, len(g.edges))
	for _, e := range g.edges {
		cp := *e
		cp.Dispatches = make([]DispatchRecord, len(e.Dispatches))
		copy(cp.Dispatches, e.Dispatches)
		out = append(out, cp)
	}
	return out
}

// ─── SSEHub ──────────────────────────────────────────────────────────────────

// SSEHub fans-out byte-slice messages to multiple per-subscriber buffered
// channels. When a subscriber's channel is full, the oldest queued message is
// dropped to prevent back-pressure on the publisher.
type SSEHub struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	subCap int
}

// NewSSEHub creates an SSEHub. subCap is the per-subscriber channel buffer size.
func NewSSEHub(subCap int) *SSEHub {
	if subCap <= 0 {
		subCap = 64
	}
	return &SSEHub{subs: make(map[chan []byte]struct{}), subCap: subCap}
}

// Subscribe returns a channel that receives SSE messages. The caller must
// call Unsubscribe when done to avoid a goroutine/memory leak.
func (h *SSEHub) Subscribe() chan []byte {
	ch := make(chan []byte, h.subCap)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes ch from the hub. The channel is intentionally NOT closed
// here: Publish snapshots the subscriber list under the mutex and then sends
// after releasing it, so closing a channel that a concurrent Publish already
// snapshotted would cause a send-on-closed-channel panic. Readers (serveSSE)
// exit via context cancellation rather than channel closure.
func (h *SSEHub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// Publish sends msg to every subscriber. Slow subscribers that cannot receive
// immediately have their oldest pending message dropped before msg is sent.
func (h *SSEHub) Publish(msg []byte) {
	h.mu.Lock()
	subs := make([]chan []byte, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// Drop oldest to make room for the new message.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

// ─── ActiveRuns ───────────────────────────────────────────────────────────────

// ActiveRuns tracks the number of in-flight agent runs per agent name.
// It implements workflow.RunTracker and the webhook.RuntimeStateProvider interface.
type ActiveRuns struct {
	mu   sync.RWMutex
	runs map[string]int // agent name → count of active concurrent runs
}

func newActiveRuns() *ActiveRuns {
	return &ActiveRuns{runs: make(map[string]int)}
}

// StartRun increments the in-flight run count for agentName.
func (a *ActiveRuns) StartRun(agentName string) {
	a.mu.Lock()
	a.runs[agentName]++
	a.mu.Unlock()
}

// FinishRun decrements the in-flight run count for agentName.
func (a *ActiveRuns) FinishRun(agentName string) {
	a.mu.Lock()
	if a.runs[agentName] > 0 {
		a.runs[agentName]--
	}
	a.mu.Unlock()
}

// IsRunning returns true when agentName has at least one in-flight run.
func (a *ActiveRuns) IsRunning(agentName string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.runs[agentName] > 0
}

// ─── Store ───────────────────────────────────────────────────────────────────

// Store is the single observability container injected throughout the daemon.
// It aggregates the ring buffers, graph, SSE hubs, and active-run tracker so
// that callers need only thread one dependency.
type Store struct {
	Events     *EventBuffer
	Traces     *TraceBuffer
	Graph      *InteractionGraph
	EventsSSE  *SSEHub
	TracesSSE  *SSEHub
	MemorySSE  *SSEHub
	ActiveRuns *ActiveRuns
}

// NewStore creates a Store with default-sized buffers.
func NewStore() *Store {
	return &Store{
		Events:     NewEventBuffer(500),
		Traces:     NewTraceBuffer(200),
		Graph:      NewInteractionGraph(100),
		EventsSSE:  NewSSEHub(64),
		TracesSSE:  NewSSEHub(64),
		MemorySSE:  NewSSEHub(32),
		ActiveRuns: newActiveRuns(),
	}
}

// IsRunning implements webhook.RuntimeStateProvider. It returns true when the
// named agent has at least one in-flight run.
func (s *Store) IsRunning(agentName string) bool {
	return s.ActiveRuns.IsRunning(agentName)
}

// ─── workflow interface implementations ──────────────────────────────────────
// Store satisfies workflow.EventRecorder, workflow.TraceRecorder, and
// workflow.GraphRecorder through the methods below.

// RecordEvent implements workflow.EventRecorder. It records the event in the
// ring buffer and fans it out to SSE subscribers.
func (s *Store) RecordEvent(at time.Time, ev workflow.Event) {
	te := TimestampedEvent{
		At:      at,
		ID:      ev.ID,
		Repo:    ev.Repo.FullName,
		Kind:    ev.Kind,
		Number:  ev.Number,
		Actor:   ev.Actor,
		Payload: ev.Payload,
	}
	s.Events.Add(te)
	if b, err := sseData(te); err == nil {
		s.EventsSSE.Publish(b)
	}
}

// RecordSpan implements workflow.TraceRecorder. It stores the completed span
// in the trace ring buffer and fans it out to SSE subscribers.
func (s *Store) RecordSpan(
	spanID, rootEventID, parentSpanID,
	agent, backend, repo, eventKind, invokedBy string,
	number, dispatchDepth int,
	queueWaitMs int64, artifactsCount int,
	startedAt, finishedAt time.Time,
	status, errMsg string,
) {
	sp := Span{
		SpanID:         spanID,
		RootEventID:    rootEventID,
		ParentSpanID:   parentSpanID,
		Agent:          agent,
		Backend:        backend,
		Repo:           repo,
		EventKind:      eventKind,
		InvokedBy:      invokedBy,
		Number:         number,
		DispatchDepth:  dispatchDepth,
		QueueWaitMs:    queueWaitMs,
		ArtifactsCount: artifactsCount,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		DurationMs:     finishedAt.Sub(startedAt).Milliseconds(),
		Status:         status,
		ErrorMsg:       errMsg,
	}
	s.Traces.Add(sp)
	if b, err := sseData(sp); err == nil {
		s.TracesSSE.Publish(b)
	}
}

// RecordDispatch implements workflow.GraphRecorder.
func (s *Store) RecordDispatch(from, to, repo string, number int, reason string) {
	s.Graph.Record(from, to, repo, number, reason)
}

// sseData encodes v as a Server-Sent Event data line.
func sseData(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", b)), nil
}
