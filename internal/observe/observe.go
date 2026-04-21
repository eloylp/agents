// Package observe provides in-memory observability data structures for the
// agents daemon: a bounded event ring-buffer, a trace ring-buffer for agent
// run spans, an agent-interaction graph, and an SSE fan-out hub.
//
// All types are safe for concurrent use. Ring buffers drop the oldest entry
// on overflow so that new writes never block.
package observe

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/workflow"
)

// ─── EventBuffer ─────────────────────────────────────────────────────────────

// TimestampedEvent is a workflow event with an ingestion timestamp attached.
// JSON field names are lowercase so both the SSE stream and the REST snapshot
// at /api/events produce an identical wire shape that the dashboard can parse
// with the same client-side interface.
type TimestampedEvent struct {
	At      time.Time      `json:"at"`
	ID      string         `json:"id"`
	Repo    string         `json:"repo"`
	Kind    string         `json:"kind"`
	Number  int            `json:"number"`
	Actor   string         `json:"actor"`
	Payload map[string]any `json:"payload,omitempty"`
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
	Summary        string    `json:"summary,omitempty"` // agent's one-line response summary
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
		cp.Dispatches = slices.Clone(e.Dispatches)
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
	db         *sql.DB
	Events     *EventBuffer
	Traces     *TraceBuffer
	Graph      *InteractionGraph
	EventsSSE  *SSEHub
	TracesSSE  *SSEHub
	MemorySSE  *SSEHub
	ActiveRuns *ActiveRuns
}

// NewStore creates a Store with default-sized buffers. When db is non-nil,
// write-through persistence to SQLite is enabled and the in-memory buffers
// are pre-filled from the database on startup.
func NewStore(db *sql.DB) *Store {
	s := &Store{
		db:         db,
		Events:     NewEventBuffer(500),
		Traces:     NewTraceBuffer(200),
		Graph:      NewInteractionGraph(100),
		EventsSSE:  NewSSEHub(64),
		TracesSSE:  NewSSEHub(64),
		MemorySSE:  NewSSEHub(32),
		ActiveRuns: newActiveRuns(),
	}
	s.LoadHistory()
	return s
}

// LoadHistory pre-fills the in-memory ring buffers from SQLite so that the
// daemon starts with recent observability data available immediately. It is
// called automatically at the end of NewStore when a database is provided.
func (s *Store) LoadHistory() {
	if s.db == nil {
		return
	}

	// Load most recent events (matching ring buffer capacity of 500).
	rows, err := s.db.Query(
		`SELECT id, at, repo, kind, number, actor, payload FROM events ORDER BY at DESC LIMIT 500`,
	)
	if err != nil {
		log.Printf("observe: load event history: %v", err)
	} else {
		var events []TimestampedEvent
		for rows.Next() {
			var te TimestampedEvent
			var payloadStr string
			if err := rows.Scan(&te.ID, &te.At, &te.Repo, &te.Kind, &te.Number, &te.Actor, &payloadStr); err != nil {
				log.Printf("observe: scan event row: %v", err)
				continue
			}
			if payloadStr != "" {
				_ = json.Unmarshal([]byte(payloadStr), &te.Payload)
			}
			events = append(events, te)
		}
		rows.Close()
		// Insert in chronological order (oldest first) so the ring buffer
		// ends up with the most recent entries at the head.
		for i := len(events) - 1; i >= 0; i-- {
			s.Events.Add(events[i])
		}
	}

	// Load most recent traces (matching ring buffer capacity of 200).
	rows, err = s.db.Query(
		`SELECT span_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error FROM traces ORDER BY started_at DESC LIMIT 200`,
	)
	if err != nil {
		log.Printf("observe: load trace history: %v", err)
	} else {
		var spans []Span
		for rows.Next() {
			var sp Span
			if err := rows.Scan(
				&sp.SpanID, &sp.RootEventID, &sp.ParentSpanID,
				&sp.Agent, &sp.Backend, &sp.Repo, &sp.Number,
				&sp.EventKind, &sp.InvokedBy, &sp.DispatchDepth,
				&sp.QueueWaitMs, &sp.ArtifactsCount, &sp.Summary,
				&sp.StartedAt, &sp.FinishedAt, &sp.DurationMs,
				&sp.Status, &sp.ErrorMsg,
			); err != nil {
				log.Printf("observe: scan trace row: %v", err)
				continue
			}
			spans = append(spans, sp)
		}
		rows.Close()
		for i := len(spans) - 1; i >= 0; i-- {
			s.Traces.Add(spans[i])
		}
	}

	// Load all dispatch history to rebuild the interaction graph.
	rows, err = s.db.Query(
		`SELECT from_agent, to_agent, repo, number, reason FROM dispatch_history ORDER BY at ASC`,
	)
	if err != nil {
		log.Printf("observe: load dispatch history: %v", err)
	} else {
		for rows.Next() {
			var from, to, repo, reason string
			var number int
			if err := rows.Scan(&from, &to, &repo, &number, &reason); err != nil {
				log.Printf("observe: scan dispatch row: %v", err)
				continue
			}
			s.Graph.Record(from, to, repo, number, reason)
		}
		rows.Close()
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
	if s.db != nil {
		go func() {
			payload, _ := json.Marshal(te.Payload)
			_, err := s.db.Exec(
				`INSERT OR IGNORE INTO events (id, at, repo, kind, number, actor, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				te.ID, te.At, te.Repo, te.Kind, te.Number, te.Actor, string(payload),
			)
			if err != nil {
				log.Printf("observe: persist event %s: %v", te.ID, err)
			}
		}()
	}
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
	queueWaitMs int64, artifactsCount int, summary string,
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
		Summary:        summary,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		DurationMs:     finishedAt.Sub(startedAt).Milliseconds(),
		Status:         status,
		ErrorMsg:       errMsg,
	}
	s.Traces.Add(sp)
	if s.db != nil {
		go func() {
			_, err := s.db.Exec(
				`INSERT OR IGNORE INTO traces (span_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				sp.SpanID, sp.RootEventID, sp.ParentSpanID,
				sp.Agent, sp.Backend, sp.Repo, sp.Number,
				sp.EventKind, sp.InvokedBy, sp.DispatchDepth,
				sp.QueueWaitMs, sp.ArtifactsCount, sp.Summary,
				sp.StartedAt, sp.FinishedAt, sp.DurationMs,
				sp.Status, sp.ErrorMsg,
			)
			if err != nil {
				log.Printf("observe: persist trace %s: %v", sp.SpanID, err)
			}
		}()
	}
	if b, err := sseData(sp); err == nil {
		s.TracesSSE.Publish(b)
	}
}

// RecordDispatch implements workflow.GraphRecorder.
func (s *Store) RecordDispatch(from, to, repo string, number int, reason string) {
	s.Graph.Record(from, to, repo, number, reason)
	if s.db != nil {
		go func() {
			_, err := s.db.Exec(
				`INSERT INTO dispatch_history (from_agent, to_agent, repo, number, reason) VALUES (?,?,?,?,?)`,
				from, to, repo, number, reason,
			)
			if err != nil {
				log.Printf("observe: persist dispatch %s->%s: %v", from, to, err)
			}
		}()
	}
}

// PublishMemoryChange emits a MemoryChangeEvent to the MemorySSE hub for the
// given agent and repo. Called by the SQLite memory backend after each write so
// the UI SSE stream stays live when the daemon runs in --db mode.
func (s *Store) PublishMemoryChange(agent, repo string) {
	ev := MemoryChangeEvent{Agent: agent, Repo: repo, Path: agent + "/" + repo}
	if b, err := sseData(ev); err == nil {
		s.MemorySSE.Publish(b)
	}
}

// sseData encodes v as a Server-Sent Event data line.
func sseData(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", b)), nil
}
