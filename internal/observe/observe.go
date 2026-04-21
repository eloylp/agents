// Package observe provides observability primitives for the agents daemon:
// SQLite-backed event, trace, and dispatch-history queries, SSE fan-out hubs,
// and an in-process active-run tracker.
//
// All types are safe for concurrent use.
package observe

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/workflow"
)

// ─── Types ───────────────────────────────────────────────────────────────────

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
// It holds the SQLite database for persistent queries, SSE hubs for live
// streaming, and the active-run tracker for ephemeral per-process state.
type Store struct {
	db         *sql.DB
	EventsSSE  *SSEHub
	TracesSSE  *SSEHub
	MemorySSE  *SSEHub
	ActiveRuns *ActiveRuns
}

// NewStore creates a Store. The db handle is used for all read/write
// operations against the events, traces, and dispatch_history tables.
func NewStore(db *sql.DB) *Store {
	return &Store{
		db:         db,
		EventsSSE:  NewSSEHub(64),
		TracesSSE:  NewSSEHub(64),
		MemorySSE:  NewSSEHub(32),
		ActiveRuns: newActiveRuns(),
	}
}

// ─── Query methods (read from SQLite) ────────────────────────────────────────

// ListEvents returns stored events ordered by time ascending (oldest first).
// When since is non-zero, only events strictly after that time are returned.
// Results are capped at 500 rows.
func (s *Store) ListEvents(since time.Time) []TimestampedEvent {
	if s.db == nil {
		return nil
	}
	var rows *sql.Rows
	var err error
	if since.IsZero() {
		rows, err = s.db.Query(
			`SELECT id, at, repo, kind, number, actor, payload FROM events ORDER BY at ASC LIMIT 500`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, at, repo, kind, number, actor, payload FROM events WHERE at > ? ORDER BY at ASC LIMIT 500`,
			since,
		)
	}
	if err != nil {
		log.Printf("observe: list events: %v", err)
		return nil
	}
	defer rows.Close()
	var out []TimestampedEvent
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
		out = append(out, te)
	}
	return out
}

// ListTraces returns stored spans ordered by started_at descending (newest
// first). Results are capped at 200 rows.
func (s *Store) ListTraces() []Span {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT span_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error FROM traces ORDER BY started_at DESC LIMIT 200`,
	)
	if err != nil {
		log.Printf("observe: list traces: %v", err)
		return nil
	}
	defer rows.Close()
	return scanSpans(rows)
}

// TracesByRootEventID returns all spans whose root_event_id matches id.
func (s *Store) TracesByRootEventID(id string) []Span {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT span_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error FROM traces WHERE root_event_id = ?`,
		id,
	)
	if err != nil {
		log.Printf("observe: traces by root event %s: %v", id, err)
		return nil
	}
	defer rows.Close()
	return scanSpans(rows)
}

// scanSpans is a shared helper that scans Span rows from a query result.
func scanSpans(rows *sql.Rows) []Span {
	var out []Span
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
		out = append(out, sp)
	}
	return out
}

// ListEdges returns the dispatch interaction graph by grouping rows from
// dispatch_history by (from_agent, to_agent) into Edge structs.
func (s *Store) ListEdges() []Edge {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT from_agent, to_agent, repo, number, reason, at FROM dispatch_history ORDER BY at ASC`,
	)
	if err != nil {
		log.Printf("observe: list edges: %v", err)
		return nil
	}
	defer rows.Close()

	edges := make(map[string]*Edge) // key: "from\x00to"
	for rows.Next() {
		var from, to, repo, reason string
		var number int
		var at time.Time
		if err := rows.Scan(&from, &to, &repo, &number, &reason, &at); err != nil {
			log.Printf("observe: scan dispatch row: %v", err)
			continue
		}
		key := from + "\x00" + to
		e, ok := edges[key]
		if !ok {
			e = &Edge{From: from, To: to}
			edges[key] = e
		}
		e.Count++
		e.Dispatches = append(e.Dispatches, DispatchRecord{At: at, Repo: repo, Number: number, Reason: reason})
	}

	out := make([]Edge, 0, len(edges))
	for _, e := range edges {
		out = append(out, *e)
	}
	return out
}

// IsRunning implements webhook.RuntimeStateProvider. It returns true when the
// named agent has at least one in-flight run.
func (s *Store) IsRunning(agentName string) bool {
	return s.ActiveRuns.IsRunning(agentName)
}

// ─── workflow interface implementations ──────────────────────────────────────
// Store satisfies workflow.EventRecorder, workflow.TraceRecorder, and
// workflow.GraphRecorder through the methods below.

// RecordEvent implements workflow.EventRecorder. It persists the event to
// SQLite and fans it out to SSE subscribers.
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

// RecordSpan implements workflow.TraceRecorder. It persists the completed span
// to SQLite and fans it out to SSE subscribers.
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
