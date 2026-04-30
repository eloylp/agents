// Package observe implements the read-only observability HTTP surface:
// events, traces, graph, dispatches, memory, and SSE streams. Handlers
// share no mutable state and depend only on narrow interfaces injected at
// construction time. The package owns the wire types and the SSE helper.
//
// The HTTP server constructs a Handler at startup and mounts its routes via
// RegisterRoutes. This isolates the observability surface from webhook
// concerns (HMAC verification, GitHub event parsing) and from CRUD writes,
// per the package layout described on issue #250.
package observe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/config"
	obstore "github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/server"
	"github.com/eloylp/agents/internal/workflow"
)

// ConfigGetter returns the current effective config. Kept as an interface
// here (rather than a *server.Server reference) so observe_test.go can
// supply a stubConfig with test-controlled cfg values.
type ConfigGetter interface {
	Config() *config.Config
}

// Handler implements the observability HTTP endpoints. Construct via New and
// mount with RegisterRoutes. Handlers are read-only and safe for concurrent
// use; the type holds no mutable state.
type Handler struct {
	store         *obstore.Store
	cfg           ConfigGetter
	provider      server.StatusProvider
	runtimeState  server.RuntimeStateProvider
	dispatchStats server.DispatchStatsProvider
	memReader     server.MemoryReader
}

// New constructs a Handler. store is required; the rest may be nil —
// handlers degrade gracefully when their dependencies are absent (no
// schedule data, all-idle node status, empty dispatch stats, /memory
// disabled).
func New(
	store *obstore.Store,
	cfg ConfigGetter,
	provider server.StatusProvider,
	runtimeState server.RuntimeStateProvider,
	dispatchStats server.DispatchStatsProvider,
	memReader server.MemoryReader,
) *Handler {
	return &Handler{
		store:         store,
		cfg:           cfg,
		provider:      provider,
		runtimeState:  runtimeState,
		dispatchStats: dispatchStats,
		memReader:     memReader,
	}
}

// RegisterRoutes mounts all observability endpoints on r. withTimeout wraps
// non-streaming handlers in an http.TimeoutHandler; SSE streaming endpoints
// are registered without the wrapper so they can hold connections open
// indefinitely (issue #173).
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/events", withTimeout(http.HandlerFunc(h.HandleEvents))).Methods(http.MethodGet)
	r.HandleFunc("/events/stream", h.HandleEventsStream)
	r.Handle("/traces", withTimeout(http.HandlerFunc(h.HandleTraces))).Methods(http.MethodGet)
	r.HandleFunc("/traces/stream", h.HandleTracesStream)
	r.Handle("/traces/{root_event_id}", withTimeout(http.HandlerFunc(h.HandleTrace))).Methods(http.MethodGet)
	r.Handle("/traces/{span_id}/steps", withTimeout(http.HandlerFunc(h.HandleTraceSteps))).Methods(http.MethodGet)
	r.Handle("/graph", withTimeout(http.HandlerFunc(h.HandleGraph))).Methods(http.MethodGet)
	r.Handle("/dispatches", withTimeout(http.HandlerFunc(h.HandleDispatches))).Methods(http.MethodGet)
	r.Handle("/memory/{agent}/{repo}", withTimeout(http.HandlerFunc(h.HandleMemory))).Methods(http.MethodGet)
	r.HandleFunc("/memory/stream", h.HandleMemoryStream)
}

// ── /dispatches ────────────────────────────────────────────────────────────

// HandleDispatches serves GET /dispatches — the current dispatch counters as
// reported by the DispatchStatsProvider. Returns an empty object when no
// provider is configured (e.g. dispatch disabled).
func (h *Handler) HandleDispatches(w http.ResponseWriter, _ *http.Request) {
	var stats workflow.DispatchStats
	if h.dispatchStats != nil {
		stats = h.dispatchStats.DispatchStats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// ── /events ────────────────────────────────────────────────────────────────

// eventJSON is the wire shape for one event in /events.
type eventJSON struct {
	At      string         `json:"at"`
	ID      string         `json:"id"`
	Repo    string         `json:"repo"`
	Kind    string         `json:"kind"`
	Number  int            `json:"number"`
	Actor   string         `json:"actor"`
	Payload map[string]any `json:"payload,omitempty"`
}

// HandleEvents serves GET /events — recent event history.
// An optional ?since=<RFC3339> query parameter filters to events after that
// time.
func (h *Handler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			since = t
		}
	}

	events := h.store.ListEvents(since)
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, eventJSON{
			At:      e.At.UTC().Format(time.RFC3339Nano),
			ID:      e.ID,
			Repo:    e.Repo,
			Kind:    e.Kind,
			Number:  e.Number,
			Actor:   e.Actor,
			Payload: e.Payload,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// HandleEventsStream serves GET /events/stream as a Server-Sent Events
// stream. Each new event is pushed as a "data: <json>\n\n" message.
func (h *Handler) HandleEventsStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, h.store.EventsSSE)
}

// ── /traces ────────────────────────────────────────────────────────────────

// HandleTraces serves GET /traces — the most recent agent run spans.
func (h *Handler) HandleTraces(w http.ResponseWriter, _ *http.Request) {
	spans := h.store.ListTraces()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spans)
}

// HandleTrace serves GET /traces/{root_event_id} — all spans for one root.
func (h *Handler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["root_event_id"]
	spans := h.store.TracesByRootEventID(id)
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spans)
}

// HandleTracesStream serves GET /traces/stream as a Server-Sent Events
// stream. Each completed span is pushed as a "data: <json>\n\n" message.
func (h *Handler) HandleTracesStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, h.store.TracesSSE)
}

// HandleTraceSteps serves GET /traces/{span_id}/steps — the tool-loop
// transcript for a single span. Returns an empty JSON array when no steps
// have been recorded (e.g. the span used a non-claude backend).
func (h *Handler) HandleTraceSteps(w http.ResponseWriter, r *http.Request) {
	spanID := mux.Vars(r)["span_id"]
	steps := h.store.ListSteps(spanID)
	if steps == nil {
		steps = []workflow.TraceStep{} // always return a JSON array, never null
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(steps)
}

// ── /graph ─────────────────────────────────────────────────────────────────

// graphJSON is the wire shape for GET /graph.
type graphJSON struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

type graphNode struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}

type graphEdge struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	Count      int              `json:"count"`
	Dispatches []dispatchRecord `json:"dispatches"`
}

type dispatchRecord struct {
	At     string `json:"at"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Reason string `json:"reason"`
}

// HandleGraph serves GET /graph — the current agent interaction graph.
// Nodes are seeded from the configured fleet (issue #151: "Nodes = agents")
// and any edge endpoints not already covered by the current config (e.g.
// agents removed from config but with recorded dispatch history).
func (h *Handler) HandleGraph(w http.ResponseWriter, _ *http.Request) {
	edges := h.store.ListEdges()

	// Build a map of the last cron error status for each agent so idle
	// agents that last exited with an error are flagged in the response.
	lastErrorByAgent := make(map[string]bool)
	if h.provider != nil {
		for _, as := range h.provider.AgentStatuses() {
			if as.LastStatus == "error" {
				lastErrorByAgent[as.Name] = true
			}
		}
	}

	nodeStatus := func(name string) string {
		if h.runtimeState != nil && h.runtimeState.IsRunning(name) {
			return "running"
		}
		if lastErrorByAgent[name] {
			return "error"
		}
		return ""
	}

	seen := make(map[string]struct{})
	if h.cfg != nil {
		for _, a := range h.cfg.Config().Agents {
			seen[a.Name] = struct{}{}
		}
	}
	for _, e := range edges {
		seen[e.From] = struct{}{}
		seen[e.To] = struct{}{}
	}
	nodes := make([]graphNode, 0, len(seen))
	for id := range seen {
		nodes = append(nodes, graphNode{ID: id, Status: nodeStatus(id)})
	}

	wireEdges := make([]graphEdge, 0, len(edges))
	for _, e := range edges {
		recs := make([]dispatchRecord, 0, len(e.Dispatches))
		for _, d := range e.Dispatches {
			recs = append(recs, dispatchRecord{
				At:     d.At.UTC().Format(time.RFC3339),
				Repo:   d.Repo,
				Number: d.Number,
				Reason: d.Reason,
			})
		}
		wireEdges = append(wireEdges, graphEdge{
			From:       e.From,
			To:         e.To,
			Count:      e.Count,
			Dispatches: recs,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(graphJSON{Nodes: nodes, Edges: wireEdges})
}

// ── /memory ────────────────────────────────────────────────────────────────

// HandleMemory serves GET /memory/{agent}/{repo} — returns the raw markdown
// content of the agent's memory for the given repo. The {repo} path segment
// is expected in the format "owner_repo" (underscore separator), matching
// both the filesystem layout and the normalised key in the SQLite memory
// store. Returns 503 when no memory reader is configured.
func (h *Handler) HandleMemory(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agent := filepath.Clean(vars["agent"])
	repo := filepath.Clean(vars["repo"])

	// Reject path traversal attempts.
	if agent == "." || repo == "." || agent == ".." || repo == ".." {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if h.memReader == nil {
		http.Error(w, "memory reader not configured", http.StatusServiceUnavailable)
		return
	}

	content, mtime, err := h.memReader.ReadMemory(agent, repo)
	if errors.Is(err, server.ErrMemoryNotFound) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not read memory", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if !mtime.IsZero() {
		w.Header().Set("X-Memory-Mtime", mtime.UTC().Format(time.RFC3339))
	}
	_, _ = w.Write([]byte(content))
}

// HandleMemoryStream serves GET /memory/stream as a Server-Sent Events
// stream that notifies subscribers when any memory file changes.
func (h *Handler) HandleMemoryStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, h.store.MemorySSE)
}

// ── SSE helper ─────────────────────────────────────────────────────────────

// defaultSSEHeartbeatInterval is how often serveSSE writes a comment to keep
// the TCP connection alive through intermediate proxies.
const defaultSSEHeartbeatInterval = 30 * time.Second

// SSEHub is the minimal subscribe/unsubscribe interface ServeSSEWithInterval
// requires. The internal/observe package's per-domain hubs (EventsSSE,
// TracesSSE, MemorySSE) all satisfy it.
type SSEHub interface {
	Subscribe() chan []byte
	Unsubscribe(chan []byte)
}

// serveSSE subscribes the current HTTP connection to hub, streams incoming
// messages, and unsubscribes on client disconnect or context cancellation.
// A periodic comment heartbeat (": heartbeat\n\n") is written every 30 s to
// keep the connection alive through proxies that close idle TCP connections
// (e.g. nginx's proxy_read_timeout).
func serveSSE(w http.ResponseWriter, r *http.Request, hub SSEHub) {
	ServeSSEWithInterval(w, r, hub, defaultSSEHeartbeatInterval)
}

// ServeSSEWithInterval is the testable core of serveSSE; callers that need a
// different heartbeat period (e.g. tests) use this directly.
//
// The function clears the per-connection write deadline that
// http.Server.WriteTimeout installs. Non-SSE routes are protected by that
// deadline (and additionally by http.TimeoutHandler); SSE streams must be
// allowed to write indefinitely, so we remove the deadline here without
// affecting other connections.
func ServeSSEWithInterval(w http.ResponseWriter, r *http.Request, hub SSEHub, heartbeatInterval time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Send a comment immediately so the client knows the stream is live.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			// SSE spec §9.2: lines beginning with ':' are comments and are
			// ignored by EventSource. Writing them periodically prevents
			// intermediate proxies from closing idle connections.
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_, err := w.Write(msg)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
