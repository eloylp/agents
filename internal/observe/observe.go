// Package observe provides observability primitives for the agents daemon:
// SQLite-backed event, trace, and dispatch-history queries, SSE fan-out hubs,
// and an in-process active-run tracker.
//
// All types are safe for concurrent use.
package observe

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// ─── Types ───────────────────────────────────────────────────────────────────

// TimestampedEvent is a workflow event with an ingestion timestamp attached.
// JSON field names are lowercase so both the SSE stream and the REST snapshot
// at /api/events produce an identical wire shape that the dashboard can parse
// with the same client-side interface.
type TimestampedEvent struct {
	At          time.Time      `json:"at"`
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Repo        string         `json:"repo"`
	Kind        string         `json:"kind"`
	Number      int            `json:"number"`
	Actor       string         `json:"actor"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// Span records the timing and outcome of a single agent run.
type Span struct {
	SpanID         string    `json:"span_id"`
	WorkspaceID    string    `json:"workspace_id"`
	RootEventID    string    `json:"root_event_id"`
	ParentSpanID   string    `json:"parent_span_id,omitempty"`
	Agent          string    `json:"agent"`
	Backend        string    `json:"backend"`
	Repo           string    `json:"repo"`
	Number         int       `json:"number"`
	EventKind      string    `json:"event_kind"`
	InvokedBy      string    `json:"invoked_by,omitempty"`
	DispatchDepth  int       `json:"dispatch_depth"`
	QueueWaitMs    int64     `json:"queue_wait_ms"`     // time from enqueue to run start
	ArtifactsCount int       `json:"artifacts_count"`   // number of artifacts produced
	Summary        string    `json:"summary,omitempty"` // agent's one-line response summary
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	DurationMs     int64     `json:"duration_ms"`
	Status         string    `json:"status"` // "success" | "error"
	ErrorMsg       string    `json:"error,omitempty"`

	// PromptSize is the uncompressed byte count of the composed prompt.
	// Surfaced on listings so the UI can show "32 KB prompt"; the body
	// is gzipped on disk and fetched lazily via /traces/{span_id}/prompt.
	PromptSize int64 `json:"prompt_size,omitempty"`

	// Token usage as reported by the AI CLI. Anthropic / Claude Code
	// emits four fields; OpenAI / Codex emits two, cache fields are
	// zero in that case. Pre-009-migration runs have all four nil
	// (preserved as omitempty).
	InputTokens      int64 `json:"input_tokens,omitempty"`
	OutputTokens     int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// DispatchRecord is one observed inter-agent dispatch.
type DispatchRecord struct {
	At          time.Time `json:"at"`
	WorkspaceID string    `json:"workspace_id"`
	Repo        string    `json:"repo"`
	Number      int       `json:"number"`
	Reason      string    `json:"reason"`
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
// It implements workflow.RunTracker; the server's observe handler reads
// IsRunning to mark agents as running in the /graph and /agents views.
type ActiveRuns struct {
	mu   sync.RWMutex
	runs map[string]int // agent name → count of active concurrent runs
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

// ─── RunRegistry ─────────────────────────────────────────────────────────────

// ActiveRun is the in-memory snapshot of a span that's currently running.
// Used by the runners view to surface in-flight rows (agent populated,
// span_id available so the UI can offer a live-stream affordance) and by
// the live-stream SSE handler to enumerate / look up the per-span hub.
//
// Lives only in memory by design, once the run completes, the canonical
// trace row in SQLite supersedes it.
type ActiveRun struct {
	SpanID      string
	EventID     string
	WorkspaceID string
	Agent       string
	Backend     string
	Repo        string
	EventKind   string
	StartedAt   time.Time
}

// runStream is the per-span pub/sub hub for live persisted TraceStep rows.
// History lives in trace_steps; this hub is only a wake-up/tail notifier.
type runStream struct {
	mu     sync.Mutex
	subs   map[chan workflow.TraceStep]struct{}
	closed bool
}

const (
	runStreamSubBufCap = 256 // per-subscriber channel buffer
)

func newRunStream() *runStream {
	return &runStream{
		subs: make(map[chan workflow.TraceStep]struct{}),
	}
}

// publish fans a persisted step out to live subscribers. A full subscriber
// channel drops the step silently; observability must not back-pressure the
// runner after the DB write has committed.
func (r *runStream) publish(step workflow.TraceStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for ch := range r.subs {
		select {
		case ch <- step:
		default:
		}
	}
}

func (r *runStream) end() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	subs := r.subs
	r.subs = make(map[chan workflow.TraceStep]struct{})
	r.mu.Unlock()

	// Close every subscriber channel so SSE handlers emit the terminal event.
	for ch := range subs {
		close(ch)
	}
}

// subscribe returns a channel for future steps. The caller MUST consume the
// channel until close or call unsubscribe to avoid leaking the subscription.
func (r *runStream) subscribe() chan workflow.TraceStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		ch := make(chan workflow.TraceStep)
		close(ch)
		return ch
	}
	ch := make(chan workflow.TraceStep, runStreamSubBufCap)
	r.subs[ch] = struct{}{}
	return ch
}

func (r *runStream) unsubscribe(ch chan workflow.TraceStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[ch]; ok {
		delete(r.subs, ch)
		// Don't close here, the caller is the consumer; end() handles
		// the close path.
	}
}

// RunRegistry tracks active agent runs in memory. Each entry carries enough
// metadata for the runners view to render an in-flight row (agent + span_id)
// and a per-span notifier for newly persisted transcript steps.
type RunRegistry struct {
	mu      sync.RWMutex
	active  map[string]*ActiveRun // span_id → active entry
	streams map[string]*runStream // span_id → live step notifier
}

func newRunRegistry() *RunRegistry {
	return &RunRegistry{
		active:  make(map[string]*ActiveRun),
		streams: make(map[string]*runStream),
	}
}

// BeginRun registers a new active run and creates its step notifier. Call
// before invoking the AI runner so the runners view can surface the span_id
// and the live-stream subscriber can tail rows from the start.
func (r *RunRegistry) BeginRun(run ActiveRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[run.SpanID] = &run
	if _, ok := r.streams[run.SpanID]; !ok {
		r.streams[run.SpanID] = newRunStream()
	}
}

// PublishStep fans one persisted step out to live subscribers. Safe to call
// before BeginRun (no-op) or after EndRun (also no-op, the stream is closed).
func (r *RunRegistry) PublishStep(spanID string, step workflow.TraceStep) {
	r.mu.RLock()
	st := r.streams[spanID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.publish(step)
}

// EndRun marks a run finished. The active-run entry and step notifier are
// removed immediately; completed streams remain readable from trace_steps.
func (r *RunRegistry) EndRun(spanID string) {
	r.mu.Lock()
	delete(r.active, spanID)
	st := r.streams[spanID]
	delete(r.streams, spanID)
	r.mu.Unlock()
	if st != nil {
		st.end()
	}
}

// ListActive returns the currently-running spans matching eventID, in
// arbitrary order. Used by the runners handler to surface in-flight
// rows (one per agent that's currently fanned out for this event).
// Returns the empty slice when no runs match.
func (r *RunRegistry) ListActive(eventID string) []ActiveRun {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ActiveRun, 0)
	for _, e := range r.active {
		if e.EventID == eventID {
			out = append(out, *e)
		}
	}
	return out
}

// SubscribeStream returns a channel that receives subsequent persisted steps.
// Returns false when no stream exists for the span because the run is not
// active. Caller must drain the channel until it closes.
func (r *RunRegistry) SubscribeStream(spanID string) (chan workflow.TraceStep, bool) {
	r.mu.RLock()
	st := r.streams[spanID]
	r.mu.RUnlock()
	if st == nil {
		return nil, false
	}
	return st.subscribe(), true
}

// UnsubscribeStream removes a per-stream subscriber. Idempotent.
func (r *RunRegistry) UnsubscribeStream(spanID string, ch chan workflow.TraceStep) {
	r.mu.RLock()
	st := r.streams[spanID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.unsubscribe(ch)
}

// ─── Store ───────────────────────────────────────────────────────────────────

// Store is the single observability container injected throughout the daemon.
// It holds the SQLite database for persistent queries, SSE hubs for live
// streaming, and the active-run tracker for ephemeral per-process state.
type Store struct {
	db         *sql.DB
	stepMu     sync.Mutex
	EventsSSE  *SSEHub
	TracesSSE  *SSEHub
	MemorySSE  *SSEHub
	ActiveRuns *ActiveRuns
	Runs       *RunRegistry
}

// NewStore creates a Store. The db handle is required for all read/write
// operations against observability tables.
func NewStore(db *sql.DB) *Store {
	if db == nil {
		panic("observe: nil db")
	}
	return &Store{
		db:         db,
		EventsSSE:  NewSSEHub(64),
		TracesSSE:  NewSSEHub(64),
		MemorySSE:  NewSSEHub(32),
		ActiveRuns: &ActiveRuns{runs: make(map[string]int)},
		Runs:       newRunRegistry(),
	}
}

// ─── Query methods (read from SQLite) ────────────────────────────────────────

// ListEvents returns stored events ordered by time ascending (oldest first).
// When since is non-zero, only events strictly after that time are returned.
// Results are capped at 500 rows.
func (s *Store) ListEvents(since time.Time) []TimestampedEvent {
	return s.ListEventsForWorkspace("", since)
}

func (s *Store) ListEventsForWorkspace(workspaceID string, since time.Time) []TimestampedEvent {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	var rows *sql.Rows
	var err error
	if since.IsZero() {
		rows, err = s.db.Query(
			`SELECT id, workspace_id, at, repo, kind, number, actor, payload FROM events WHERE workspace_id = ? ORDER BY at ASC LIMIT 500`,
			workspaceID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, workspace_id, at, repo, kind, number, actor, payload FROM events WHERE workspace_id = ? AND at > ? ORDER BY at ASC LIMIT 500`,
			workspaceID, since,
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
		if err := rows.Scan(&te.ID, &te.WorkspaceID, &te.At, &te.Repo, &te.Kind, &te.Number, &te.Actor, &payloadStr); err != nil {
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

// spanColumns is the column list used by every read query, kept in
// one place so adding a new column means editing one line. The
// prompt_gz blob is intentionally excluded; bodies are fetched on
// demand via PromptForSpan to keep listings small.
const spanColumns = `span_id, workspace_id, root_event_id, parent_span_id, agent, backend, repo, number,
	event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary,
	started_at, finished_at, duration_ms, status, error,
	prompt_size, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens`

// ListTraces returns stored spans ordered by started_at descending (newest
// first). Results are capped at 200 rows.
func (s *Store) ListTraces() []Span {
	return s.ListTracesForWorkspace("")
}

func (s *Store) ListTracesForWorkspace(workspaceID string) []Span {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	rows, err := s.db.Query(
		`SELECT `+spanColumns+` FROM traces WHERE workspace_id = ? ORDER BY started_at DESC LIMIT 200`,
		workspaceID,
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
	return s.TracesByRootEventIDForWorkspace("", id)
}

func (s *Store) TracesByRootEventIDForWorkspace(workspaceID, id string) []Span {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	rows, err := s.db.Query(
		`SELECT `+spanColumns+` FROM traces WHERE workspace_id = ? AND root_event_id = ?`,
		workspaceID, id,
	)
	if err != nil {
		log.Printf("observe: traces by root event %s: %v", id, err)
		return nil
	}
	defer rows.Close()
	return scanSpans(rows)
}

// PromptForSpan returns the decompressed composed prompt for a span,
// or ("", nil) when no prompt was stored (pre-migration rows or runs
// that never recorded one). The blob is gzipped on disk; this method
// is the single read site so the compression detail stays here.
func (s *Store) PromptForSpan(spanID string) (string, error) {
	var blob []byte
	err := s.db.QueryRow(`SELECT prompt_gz FROM traces WHERE span_id = ?`, spanID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) || len(blob) == 0 {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("observe: fetch prompt %s: %w", spanID, err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return "", fmt.Errorf("observe: gunzip prompt %s: %w", spanID, err)
	}
	defer gr.Close()
	out, err := io.ReadAll(gr)
	if err != nil {
		return "", fmt.Errorf("observe: read prompt %s: %w", spanID, err)
	}
	return string(out), nil
}

// scanSpans is a shared helper that scans Span rows from a query result.
// The token columns are sql.NullInt64 because pre-migration rows have
// NULL, we materialise NULL to zero, the JSON layer drops zero via
// omitempty so the UI can detect "not recorded".
func scanSpans(rows *sql.Rows) []Span {
	var out []Span
	for rows.Next() {
		var sp Span
		var promptSize, inTok, outTok, cacheR, cacheW sql.NullInt64
		if err := rows.Scan(
			&sp.SpanID, &sp.WorkspaceID, &sp.RootEventID, &sp.ParentSpanID,
			&sp.Agent, &sp.Backend, &sp.Repo, &sp.Number,
			&sp.EventKind, &sp.InvokedBy, &sp.DispatchDepth,
			&sp.QueueWaitMs, &sp.ArtifactsCount, &sp.Summary,
			&sp.StartedAt, &sp.FinishedAt, &sp.DurationMs,
			&sp.Status, &sp.ErrorMsg,
			&promptSize, &inTok, &outTok, &cacheR, &cacheW,
		); err != nil {
			log.Printf("observe: scan trace row: %v", err)
			continue
		}
		sp.PromptSize = promptSize.Int64
		sp.InputTokens = inTok.Int64
		sp.OutputTokens = outTok.Int64
		sp.CacheReadTokens = cacheR.Int64
		sp.CacheWriteTokens = cacheW.Int64
		out = append(out, sp)
	}
	return out
}

// ListEdges returns the dispatch interaction graph by grouping rows from
// dispatch_history by (from_agent, to_agent) into Edge structs.
func (s *Store) ListEdges() []Edge {
	return s.ListEdgesForWorkspace("")
}

func (s *Store) ListEdgesForWorkspace(workspaceID string) []Edge {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	rows, err := s.db.Query(
		`SELECT from_agent, to_agent, repo, number, reason, at FROM dispatch_history WHERE workspace_id = ? ORDER BY at ASC`,
		workspaceID,
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
		e.Dispatches = append(e.Dispatches, DispatchRecord{At: at, WorkspaceID: workspaceID, Repo: repo, Number: number, Reason: reason})
	}

	out := make([]Edge, 0, len(edges))
	for _, e := range edges {
		out = append(out, *e)
	}
	return out
}

// IsRunning returns true when the named agent has at least one in-flight
// run. Used by the server's observe handler to flag running agents in the
// /graph view.
func (s *Store) IsRunning(agentName string) bool {
	return s.ActiveRuns.IsRunning(agentName)
}

// ─── workflow interface implementations ──────────────────────────────────────
// Store satisfies workflow.EventRecorder, workflow.TraceRecorder, and
// workflow.GraphRecorder through the methods below.

// BeginRun implements workflow.RunStreamPublisher. Forwards to the in-memory
// RunRegistry so the runners view can surface in-flight rows.
func (s *Store) BeginRun(in workflow.BeginRunInput) {
	s.Runs.BeginRun(ActiveRun{
		SpanID:      in.SpanID,
		EventID:     in.EventID,
		WorkspaceID: fleet.NormalizeWorkspaceID(in.WorkspaceID),
		Agent:       in.Agent,
		Backend:     in.Backend,
		Repo:        in.Repo,
		EventKind:   in.EventKind,
		StartedAt:   in.StartedAt,
	})
}

// EndRun implements workflow.RunStreamPublisher. Marks the run finished in
// the registry.
func (s *Store) EndRun(spanID string) {
	s.Runs.EndRun(spanID)
}

// RecordEvent implements workflow.EventRecorder. It persists the event to
// SQLite and fans it out to SSE subscribers.
func (s *Store) RecordEvent(at time.Time, ev workflow.Event) {
	te := TimestampedEvent{
		At:          at,
		ID:          ev.ID,
		WorkspaceID: fleet.NormalizeWorkspaceID(ev.WorkspaceID),
		Repo:        ev.Repo.FullName,
		Kind:        ev.Kind,
		Number:      ev.Number,
		Actor:       ev.Actor,
		Payload:     ev.Payload,
	}
	if s.db != nil {
		go func() {
			payload, _ := json.Marshal(te.Payload)
			_, err := s.db.Exec(
				`INSERT OR IGNORE INTO events (id, workspace_id, at, repo, kind, number, actor, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				te.ID, te.WorkspaceID, te.At, te.Repo, te.Kind, te.Number, te.Actor, string(payload),
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
// (including the gzipped prompt and token usage) to SQLite and fans the
// summary out to SSE subscribers.
func (s *Store) RecordSpan(in workflow.SpanInput) {
	sp := Span{
		SpanID:           in.SpanID,
		WorkspaceID:      fleet.NormalizeWorkspaceID(in.WorkspaceID),
		RootEventID:      in.RootEventID,
		ParentSpanID:     in.ParentSpanID,
		Agent:            in.Agent,
		Backend:          in.Backend,
		Repo:             in.Repo,
		EventKind:        in.EventKind,
		InvokedBy:        in.InvokedBy,
		Number:           in.Number,
		DispatchDepth:    in.DispatchDepth,
		QueueWaitMs:      in.QueueWaitMs,
		ArtifactsCount:   in.ArtifactsCount,
		Summary:          in.Summary,
		StartedAt:        in.StartedAt,
		FinishedAt:       in.FinishedAt,
		DurationMs:       in.FinishedAt.Sub(in.StartedAt).Milliseconds(),
		Status:           in.Status,
		ErrorMsg:         in.ErrorMsg,
		PromptSize:       int64(len(in.Prompt)),
		InputTokens:      in.InputTokens,
		OutputTokens:     in.OutputTokens,
		CacheReadTokens:  in.CacheReadTokens,
		CacheWriteTokens: in.CacheWriteTokens,
	}
	var promptGz []byte
	if in.Prompt != "" {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write([]byte(in.Prompt)); err != nil {
			log.Printf("observe: gzip prompt %s: %v", sp.SpanID, err)
		} else if err := gw.Close(); err != nil {
			log.Printf("observe: gzip flush prompt %s: %v", sp.SpanID, err)
		} else {
			promptGz = buf.Bytes()
		}
	}
	if s.db != nil {
		go func() {
			_, err := s.db.Exec(
				`INSERT OR IGNORE INTO traces (span_id, workspace_id, root_event_id, parent_span_id, agent, backend, repo, number, event_kind, invoked_by, dispatch_depth, queue_wait_ms, artifacts_count, summary, started_at, finished_at, duration_ms, status, error, prompt_gz, prompt_size, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				sp.SpanID, sp.WorkspaceID, sp.RootEventID, sp.ParentSpanID,
				sp.Agent, sp.Backend, sp.Repo, sp.Number,
				sp.EventKind, sp.InvokedBy, sp.DispatchDepth,
				sp.QueueWaitMs, sp.ArtifactsCount, sp.Summary,
				sp.StartedAt, sp.FinishedAt, sp.DurationMs,
				sp.Status, sp.ErrorMsg,
				promptGz, sp.PromptSize,
				sp.InputTokens, sp.OutputTokens, sp.CacheReadTokens, sp.CacheWriteTokens,
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
func (s *Store) RecordDispatch(workspaceID, from, to, repo string, number int, reason string) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	if s.db != nil {
		go func() {
			_, err := s.db.Exec(
				`INSERT INTO dispatch_history (workspace_id, from_agent, to_agent, repo, number, reason) VALUES (?,?,?,?,?,?)`,
				workspaceID, from, to, repo, number, reason,
			)
			if err != nil {
				log.Printf("observe: persist dispatch %s->%s: %v", from, to, err)
			}
		}()
	}
}

// RecordStep implements workflow.StepRecorder. It persists one tool-loop
// transcript step to SQLite, then fans the committed row out to live stream
// subscribers. Steps are stored sequentially and capped at 100 per span.
func (s *Store) RecordStep(spanID string, step workflow.TraceStep) {
	if step.Kind == "" {
		step.Kind = workflow.StepKindTool
	}
	inserted := false
	s.stepMu.Lock()
	var idx int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trace_steps WHERE span_id=?`, spanID).Scan(&idx)
	if err != nil {
		log.Printf("observe: count trace steps for %s: %v", spanID, err)
	} else if idx < 100 {
		_, err = s.db.Exec(
			`INSERT INTO trace_steps (span_id, step_index, kind, tool_name, input_summary, output_summary, duration_ms) VALUES (?,?,?,?,?,?,?)`,
			spanID, idx, step.Kind, step.ToolName, step.InputSummary, step.OutputSummary, step.DurationMs,
		)
		if err != nil {
			log.Printf("observe: insert trace step %d for %s: %v", idx, spanID, err)
		} else {
			inserted = true
		}
	}
	s.stepMu.Unlock()
	if inserted && s.Runs != nil {
		s.Runs.PublishStep(spanID, step)
	}
}

// RecordSteps implements workflow.StepRecorder. It persists the tool-loop
// transcript steps for a completed span to SQLite. Steps are stored
// sequentially (step_index 0, 1, …) and capped at 100 per span.
// The write is synchronous so that a subsequent ListSteps call (e.g. from the
// UI on first accordion open) always observes the committed rows.
func (s *Store) RecordSteps(spanID string, steps []workflow.TraceStep) {
	if len(steps) == 0 {
		return
	}
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("observe: begin trace steps tx for %s: %v", spanID, err)
		return
	}
	for i, step := range steps {
		if i >= 100 {
			break
		}
		kind := step.Kind
		if kind == "" {
			kind = workflow.StepKindTool
		}
		if _, err := tx.Exec(
			`INSERT INTO trace_steps (span_id, step_index, kind, tool_name, input_summary, output_summary, duration_ms) VALUES (?,?,?,?,?,?,?)`,
			spanID, i, kind, step.ToolName, step.InputSummary, step.OutputSummary, step.DurationMs,
		); err != nil {
			log.Printf("observe: insert trace step %d for %s: %v", i, spanID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("observe: commit trace steps for %s: %v", spanID, err)
		_ = tx.Rollback()
	}
}

// ListSteps returns the tool-loop transcript steps for a span, ordered by
// step_index ascending. Returns nil when no steps exist.
func (s *Store) ListSteps(spanID string) []workflow.TraceStep {
	return s.listSteps(spanID)
}

func (s *Store) listSteps(spanID string) []workflow.TraceStep {
	rows, err := s.db.Query(
		`SELECT kind, tool_name, input_summary, output_summary, duration_ms FROM trace_steps WHERE span_id=? ORDER BY step_index ASC`,
		spanID,
	)
	if err != nil {
		log.Printf("observe: list steps for %s: %v", spanID, err)
		return nil
	}
	defer rows.Close()
	var out []workflow.TraceStep
	for rows.Next() {
		var step workflow.TraceStep
		if err := rows.Scan(&step.Kind, &step.ToolName, &step.InputSummary, &step.OutputSummary, &step.DurationMs); err != nil {
			log.Printf("observe: scan step for %s: %v", spanID, err)
			continue
		}
		out = append(out, step)
	}
	return out
}

// SubscribeSteps returns a replay snapshot and, when the run is still active,
// a channel for subsequent persisted steps. The snapshot and subscription are
// ordered under stepMu so a caller cannot miss rows committed between replay
// and live tail subscription.
func (s *Store) SubscribeSteps(spanID string) ([]workflow.TraceStep, chan workflow.TraceStep, bool) {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	steps := s.listSteps(spanID)
	if s.Runs == nil {
		return steps, nil, false
	}
	ch, active := s.Runs.SubscribeStream(spanID)
	return steps, ch, active
}

// PublishMemoryChange emits a MemoryChangeEvent to the MemorySSE hub for the
// given workspace, agent, and repo. Called by the SQLite memory backend after
// each write so the UI SSE stream stays live when the daemon runs in --db mode.
func (s *Store) PublishMemoryChange(workspace, agent, repo string) {
	workspace = fleet.NormalizeWorkspaceID(workspace)
	ev := MemoryChangeEvent{Workspace: workspace, Agent: agent, Repo: repo, Path: workspace + "/" + agent + "/" + repo}
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
