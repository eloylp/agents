// Package runners implements the /runners HTTP surface: paginated
// listing of unit-of-work rows ("runners") plus operator actions for
// removing or retrying individual events. The underlying persistence is
// the durable event_queue table (one row per event); this handler
// JOINs each row with the trace spans recorded by observe so that
// fanout (one event → N agents) shows up as N rows on the wire.
//
// Row composition rule:
//   - event_queue.completed_at IS NULL  → emit 1 row (agent=null,
//     status=enqueued|running). The worker has not finished fanning
//     out; we don't yet know which agents will run.
//   - event_queue.completed_at IS NOT NULL → query traces for the
//     event id. Emit 1 row per trace span (status=success|error,
//     agent populated). Events that completed with 0 traces (webhook
//     with no matching binding) are skipped — nothing actually ran,
//     so listing them on a "runners" page would be misleading.
//
// Retry / delete operate on the underlying event_queue row, not on a
// specific trace. Retry copies the event blob into a new row and
// pushes onto the channel — the source row stays as audit history.
// Delete removes the queue row best-effort: the in-memory channel
// buffer may still hold a copy a worker will dequeue. Both operations
// are event-level even when the row appears multiple times on the
// wire (one per fanned-out agent); the UI surfaces this in the
// expanded row's note.
package runners

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// Handler implements the /runners HTTP endpoints and exposes typed
// methods the MCP tools call into.
type Handler struct {
	store    *store.Store
	channels *workflow.DataChannels
	traces   *observe.Store
	logger   zerolog.Logger
}

// New constructs a Handler. channels is the runtime data-channel
// instance retries push onto. traces is the observe store used to
// JOIN per-agent run details into each runner row.
func New(st *store.Store, channels *workflow.DataChannels, traces *observe.Store, logger zerolog.Logger) *Handler {
	return &Handler{
		store:    st,
		channels: channels,
		traces:   traces,
		logger:   logger.With().Str("component", "server_runners").Logger(),
	}
}

// RegisterRoutes mounts the runners endpoints on r.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/runners", withTimeout(http.HandlerFunc(h.handleList))).Methods(http.MethodGet)
	r.Handle("/runners/{id}", withTimeout(http.HandlerFunc(h.handleDelete))).Methods(http.MethodDelete)
	r.Handle("/runners/{id}/retry", withTimeout(http.HandlerFunc(h.handleRetry))).Methods(http.MethodPost)
}

// RunnerRow is the wire shape of one runner — either an in-flight event
// (no traces yet, agent=null) or one fanned-out agent run (trace fields
// populated). Status is the unified lifecycle:
//   - "enqueued" / "running": event is in flight, no trace yet
//   - "success" / "error":     trace exists, run finished with that outcome
type RunnerRow struct {
	ID          int64           `json:"id"`
	EventID     string          `json:"event_id"`
	Kind        string          `json:"kind"`
	Repo        string          `json:"repo"`
	Number      int             `json:"number,omitempty"`
	Actor       string          `json:"actor,omitempty"`
	TargetAgent string          `json:"target_agent,omitempty"`
	Status      string          `json:"status"`
	EnqueuedAt  time.Time       `json:"enqueued_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`

	// Trace-derived fields, populated only when CompletedAt != nil and a
	// matching span exists. Agent is the canonical "which runner is this".
	Agent       string `json:"agent,omitempty"`
	SpanID      string `json:"span_id,omitempty"`
	RunDuration int64  `json:"run_duration_ms,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// ListResponse is the wire shape returned by GET /runners. Total counts
// queue rows (events) under the same filter — not output rows, since a
// completed event can produce multiple rows after the trace JOIN.
type ListResponse struct {
	Runners []RunnerRow `json:"runners"`
	Total   int         `json:"total"`
	Limit   int         `json:"limit"`
	Offset  int         `json:"offset"`
}

// RetryResponse is the wire shape returned by POST /runners/{id}/retry.
type RetryResponse struct {
	NewID int64 `json:"new_id"`
}

// ErrRunnerRunning is returned by Retry when the source event is still
// in-flight. Callers map it to 409 (HTTP) or to a tool error (MCP).
var ErrRunnerRunning = errors.New("runners: cannot retry running event")

// List returns one page of runner rows plus the total event count
// under the same filter. status accepts "" / "enqueued" / "running" /
// "completed" — these gate the underlying event_queue rows. Other
// values return an error.
//
// Each event_queue row produces 0..N output rows depending on whether
// traces have been recorded for it (see package doc).
func (h *Handler) List(status string, limit, offset int) (ListResponse, error) {
	st := store.RunnerStatus(status)
	switch st {
	case "", store.RunnerEnqueued, store.RunnerRunning, store.RunnerCompleted:
	default:
		return ListResponse{}, fmt.Errorf("invalid status %q", status)
	}
	events, err := h.store.ListRunners(st, limit, offset)
	if err != nil {
		return ListResponse{}, err
	}
	total, err := h.store.CountRunners(st)
	if err != nil {
		return ListResponse{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows := make([]RunnerRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, h.expand(ev)...)
	}
	return ListResponse{Runners: rows, Total: total, Limit: limit, Offset: offset}, nil
}

// expand turns one event_queue row into 0..N RunnerRows by JOINing
// with the traces store. See package doc for the rule.
func (h *Handler) expand(ev store.RunnerRecord) []RunnerRow {
	base := RunnerRow{
		ID:          ev.ID,
		EventID:     ev.EventID,
		Kind:        ev.Kind,
		Repo:        ev.Repo,
		Number:      ev.Number,
		Actor:       ev.Actor,
		TargetAgent: ev.TargetAgent,
		EnqueuedAt:  ev.EnqueuedAt,
		StartedAt:   ev.StartedAt,
		CompletedAt: ev.CompletedAt,
		Payload:     ev.Payload,
	}
	// Not yet completed: emit one in-flight row with no trace fields.
	if ev.CompletedAt == nil {
		base.Status = string(ev.Status)
		return []RunnerRow{base}
	}
	// Completed: JOIN with traces. Each span becomes one row.
	if h.traces == nil || ev.EventID == "" {
		return nil
	}
	spans := h.traces.TracesByRootEventID(ev.EventID)
	if len(spans) == 0 {
		// Event completed without spawning any runner (e.g. webhook with no
		// matching binding). Skip — listing it under "runners" would be
		// misleading.
		return nil
	}
	out := make([]RunnerRow, 0, len(spans))
	for _, sp := range spans {
		row := base
		row.Agent = sp.Agent
		row.SpanID = sp.SpanID
		row.RunDuration = sp.DurationMs
		row.Summary = sp.Summary
		row.Status = sp.Status // "success" | "error"
		out = append(out, row)
	}
	return out
}

// Delete removes the row with id. Returns store.ErrRunnerNotFound when
// the row was already gone — the handler maps that to 404.
func (h *Handler) Delete(id int64) error {
	if _, err := h.store.GetRunner(id); err != nil {
		return err
	}
	return h.store.DeleteRunner(id)
}

// Retry re-pushes the row's blob as a new event. Returns the new row's
// id on success, ErrRunnerRunning if the source is in-flight, or
// store.ErrRunnerNotFound if the source has been deleted.
func (h *Handler) Retry(r *http.Request, id int64) (int64, error) {
	rec, err := h.store.GetRunner(id)
	if err != nil {
		return 0, err
	}
	if rec.Status == store.RunnerRunning {
		return 0, ErrRunnerRunning
	}
	blob, err := h.store.ReadQueuedEvent(id)
	if err != nil {
		return 0, err
	}
	var ev workflow.Event
	if err := json.Unmarshal([]byte(blob), &ev); err != nil {
		return 0, fmt.Errorf("runners retry: unmarshal: %w", err)
	}
	// Reset enqueue stamp so the retried row's EnqueuedAt reflects the
	// retry instant — original timing stays preserved on the source row.
	ev.EnqueuedAt = time.Time{}
	newID, err := h.channels.PushEvent(r.Context(), ev)
	if err != nil {
		return 0, err
	}
	return newID, nil
}

// ── HTTP handlers ──────────────────────────────────────────────────────────

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	resp, err := h.List(q.Get("status"), limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.Delete(id); err != nil {
		if errors.Is(err, store.ErrRunnerNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	newID, err := h.Retry(r, id)
	switch {
	case errors.Is(err, store.ErrRunnerNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrRunnerRunning):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, workflow.ErrEventQueueFull):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, workflow.ErrQueueClosed):
		writeError(w, http.StatusServiceUnavailable, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, RetryResponse{NewID: newID})
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func pathID(r *http.Request) (int64, error) {
	raw := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q", raw)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
