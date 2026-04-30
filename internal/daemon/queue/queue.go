// Package queue implements the /queue HTTP surface: paginated listing of
// the durable event_queue table plus operator actions for removing or
// retrying individual rows. All three handlers also have method
// equivalents the MCP tools call into so the wire shape is defined in
// exactly one place.
//
// Retry semantics. A retry reads the original row's blob, clears its
// EnqueuedAt so the new row gets a fresh stamp, and re-pushes through
// PushEvent. That inserts a new event_queue row and signals workers
// through the channel — the original row stays as audit history. Rows
// in the running state cannot be retried (409): a worker is already
// processing them and a duplicate would race.
//
// Delete is best-effort. It removes the row, but the in-memory channel
// buffer may still hold a copy of the QueuedEvent that a worker will
// dequeue and run. This is documented and accepted: the operator's
// intent ("don't keep this row around") is honoured for the table /
// listing; the at-most-one-run guarantee is the queue's job, not the
// delete handler's.
package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// Handler implements the /queue HTTP endpoints and exposes typed
// methods the MCP tools call into.
type Handler struct {
	store    *store.Store
	channels *workflow.DataChannels
	logger   zerolog.Logger
}

// New constructs a Handler. channels is the runtime data-channel
// instance retries push onto.
func New(st *store.Store, channels *workflow.DataChannels, logger zerolog.Logger) *Handler {
	return &Handler{
		store:    st,
		channels: channels,
		logger:   logger.With().Str("component", "server_queue").Logger(),
	}
}

// RegisterRoutes mounts the queue endpoints on r.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/queue", withTimeout(http.HandlerFunc(h.handleList))).Methods(http.MethodGet)
	r.Handle("/queue/{id}", withTimeout(http.HandlerFunc(h.handleDelete))).Methods(http.MethodDelete)
	r.Handle("/queue/{id}/retry", withTimeout(http.HandlerFunc(h.handleRetry))).Methods(http.MethodPost)
}

// ListResponse is the wire shape returned by GET /queue.
type ListResponse struct {
	Events []store.QueueEventRecord `json:"events"`
	Total  int                      `json:"total"`
	Limit  int                      `json:"limit"`
	Offset int                      `json:"offset"`
}

// RetryResponse is the wire shape returned by POST /queue/{id}/retry.
type RetryResponse struct {
	NewID int64 `json:"new_id"`
}

// ErrEventRunning is returned by Retry when the source row is still in
// the running state. Callers map it to 409 (HTTP) or to a tool error
// (MCP). Defined as a sentinel so the mapping can be centralised.
var ErrEventRunning = errors.New("queue: cannot retry running event")

// List returns one page of queue rows plus the total under the same
// filter. status accepts "" / "enqueued" / "running" / "completed";
// other values return an error.
func (h *Handler) List(status string, limit, offset int) (ListResponse, error) {
	st, err := parseStatus(status)
	if err != nil {
		return ListResponse{}, err
	}
	events, err := h.store.ListQueueEvents(st, limit, offset)
	if err != nil {
		return ListResponse{}, err
	}
	total, err := h.store.CountQueueEvents(st)
	if err != nil {
		return ListResponse{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return ListResponse{Events: events, Total: total, Limit: limit, Offset: offset}, nil
}

// Delete removes the row with id. Returns store.ErrEventNotFound when
// the row was already gone — the handler maps that to 404.
func (h *Handler) Delete(id int64) error {
	if _, err := h.store.GetQueueEvent(id); err != nil {
		return err
	}
	return h.store.DeleteQueuedEvent(id)
}

// Retry re-pushes the row's blob as a new event. Returns the new row's
// id on success, ErrEventRunning if the source is in-flight, or
// ErrEventNotFound if the source has been deleted.
func (h *Handler) Retry(r *http.Request, id int64) (int64, error) {
	rec, err := h.store.GetQueueEvent(id)
	if err != nil {
		return 0, err
	}
	if rec.Status == store.QueueEventRunning {
		return 0, ErrEventRunning
	}
	blob, err := h.store.ReadQueuedEvent(id)
	if err != nil {
		return 0, err
	}
	var ev workflow.Event
	if err := json.Unmarshal([]byte(blob), &ev); err != nil {
		return 0, fmt.Errorf("queue retry: unmarshal: %w", err)
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
		if errors.Is(err, store.ErrEventNotFound) {
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
	case errors.Is(err, store.ErrEventNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrEventRunning):
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

func parseStatus(s string) (store.QueueEventStatus, error) {
	switch s {
	case "", "enqueued", "running", "completed":
		return store.QueueEventStatus(s), nil
	}
	return "", fmt.Errorf("invalid status %q", s)
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
