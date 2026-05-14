// Package observe implements the read-only observability HTTP surface:
// events, traces, graph, dispatches, memory, and SSE streams. The package
// owns the wire types and the SSE helper.
//
// The composing daemon constructs a Handler with concrete pointers to every
// component the observability views aggregate from, the daemon ships as
// one binary and these are the same instances the rest of the daemon uses.
// Tests build the same shape against a tempdir SQLite, mirroring the
// fixture pattern internal/mcp uses.
package observe

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/fleet"
	obstore "github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// Handler implements the observability HTTP endpoints. Handlers are
// read-only and safe for concurrent use; the type holds no mutable state.
type Handler struct {
	events    *obstore.Store // events/traces/SSE pub-sub
	store     *store.Store   // fleet data access (agents for graph nodes)
	sched     *scheduler.Scheduler
	engine    *workflow.Engine
	memReader *store.MemoryReader
	logger    zerolog.Logger
}

// New constructs a Handler. All components are concrete pointers to the
// daemon's running instances.
func New(
	events *obstore.Store,
	st *store.Store,
	sched *scheduler.Scheduler,
	engine *workflow.Engine,
	memReader *store.MemoryReader,
	logger zerolog.Logger,
) *Handler {
	return &Handler{
		events:    events,
		store:     st,
		sched:     sched,
		engine:    engine,
		memReader: memReader,
		logger:    logger.With().Str("component", "server_observe").Logger(),
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
	r.Handle("/traces/{span_id}/prompt", withTimeout(http.HandlerFunc(h.HandleTracePrompt))).Methods(http.MethodGet)
	r.HandleFunc("/traces/{span_id}/stream", h.HandleTraceStream)
	r.Handle("/graph", withTimeout(http.HandlerFunc(h.HandleGraph))).Methods(http.MethodGet)
	r.Handle("/dispatches", withTimeout(http.HandlerFunc(h.HandleDispatches))).Methods(http.MethodGet)
	r.Handle("/memory/{agent}/{repo}", withTimeout(http.HandlerFunc(h.HandleMemory))).Methods(http.MethodGet)
	r.HandleFunc("/memory/stream", h.HandleMemoryStream)
}

// ── /dispatches ────────────────────────────────────────────────────────────

// HandleDispatches serves GET /dispatches, the current dispatch counters as
// reported by the DispatchStatsProvider. Returns an empty object when no
// provider is configured (e.g. dispatch disabled).
func (h *Handler) HandleDispatches(w http.ResponseWriter, _ *http.Request) {
	var stats workflow.DispatchStats
	if h.engine != nil {
		stats = h.engine.DispatchStats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// ── /events ────────────────────────────────────────────────────────────────

// eventJSON is the wire shape for one event in /events. Agents is a
// JOIN against the traces store: each event_id resolves to the set of
// agent names that ran (or are running) for it. Empty for events that
// have not yet fanned out, or webhooks that matched no binding.
type eventJSON struct {
	At          string         `json:"at"`
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Repo        string         `json:"repo"`
	Kind        string         `json:"kind"`
	Number      int            `json:"number"`
	Actor       string         `json:"actor"`
	Payload     map[string]any `json:"payload,omitempty"`
	Agents      []string       `json:"agents,omitempty"`
}

// HandleEvents serves GET /events, recent event history.
// An optional ?since=<RFC3339> query parameter filters to events after that
// time.
func (h *Handler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			since = t
		}
	}

	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	events := h.events.ListEventsForWorkspace(workspaceID, since)
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, eventJSON{
			At:          e.At.UTC().Format(time.RFC3339Nano),
			ID:          e.ID,
			WorkspaceID: e.WorkspaceID,
			Repo:        e.Repo,
			Kind:        e.Kind,
			Number:      e.Number,
			Actor:       e.Actor,
			Payload:     e.Payload,
			Agents:      agentsForEvent(h.events, workspaceID, e.ID),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// HandleEventsStream serves GET /events/stream as a Server-Sent Events
// stream. Each new event is pushed as a "data: <json>\n\n" message.
func (h *Handler) HandleEventsStream(w http.ResponseWriter, r *http.Request) {
	serveWorkspaceSSE(w, r, h.events.EventsSSE)
}

// agentsForEvent resolves the set of agents that ran (or are running)
// for a given event id by querying the traces store. Empty when no
// span has been recorded yet, either the event hasn't been picked up,
// or its run hasn't reached the recording site, or no binding matched.
// De-duplicated; preserves trace insertion order.
func agentsForEvent(s *obstore.Store, workspaceID, eventID string) []string {
	if s == nil || eventID == "" {
		return nil
	}
	spans := s.TracesByRootEventIDForWorkspace(workspaceID, eventID)
	if len(spans) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(spans))
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		if sp.Agent == "" {
			continue
		}
		if _, ok := seen[sp.Agent]; ok {
			continue
		}
		seen[sp.Agent] = struct{}{}
		out = append(out, sp.Agent)
	}
	return out
}

// ── /traces ────────────────────────────────────────────────────────────────

// HandleTraces serves GET /traces, the most recent agent run spans.
func (h *Handler) HandleTraces(w http.ResponseWriter, r *http.Request) {
	spans := h.events.ListTracesForWorkspace(r.URL.Query().Get("workspace"))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spans)
}

// HandleTrace serves GET /traces/{root_event_id}, all spans for one root.
func (h *Handler) HandleTrace(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["root_event_id"]
	spans := h.events.TracesByRootEventIDForWorkspace(r.URL.Query().Get("workspace"), id)
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
	serveWorkspaceSSE(w, r, h.events.TracesSSE)
}

// HandleTraceSteps serves GET /traces/{span_id}/steps, the tool-loop
// transcript for a single span. Returns an empty JSON array when no steps
// have been recorded (e.g. the span used a non-claude backend).
func (h *Handler) HandleTraceSteps(w http.ResponseWriter, r *http.Request) {
	spanID := mux.Vars(r)["span_id"]
	steps := h.events.ListSteps(spanID)
	if steps == nil {
		steps = []workflow.TraceStep{} // always return a JSON array, never null
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(steps)
}

// HandleTraceStream serves GET /traces/{span_id}/stream as Server-Sent Events
// streaming persisted TraceStep rows for one span. It replays rows already in
// trace_steps, then live-tails committed rows until the run ends or the client
// disconnects. Completed spans with persisted steps remain streamable.
func (h *Handler) HandleTraceStream(w http.ResponseWriter, r *http.Request) {
	spanID := mux.Vars(r)["span_id"]
	steps, ch, active := h.events.SubscribeSteps(spanID)
	if !active && len(steps) == 0 {
		http.NotFound(w, r)
		return
	}
	if active {
		defer h.events.Runs.UnsubscribeStream(spanID, ch)
	}

	// SSE headers + flush controller. Mirrors serveSSE plumbing, kept inline
	// because the data source is a persisted replay plus a per-span channel,
	// not a global hub.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	send := func(step workflow.TraceStep) bool {
		payload, err := json.Marshal(step)
		if err != nil {
			h.logger.Debug().Err(err).Str("span_id", spanID).Msg("trace stream: marshal step failed")
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			h.logger.Debug().Err(err).Str("span_id", spanID).Msg("trace stream: write step failed")
			return false
		}
		flusher.Flush()
		return true
	}

	// Replay persisted steps first so a late-joining client sees what the run
	// did before they connected. After replay, tail committed rows until close.
	for _, step := range steps {
		if !send(step) {
			return
		}
	}
	if !active {
		_, _ = fmt.Fprint(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	heartbeat := time.NewTicker(defaultSSEHeartbeatInterval)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				h.logger.Debug().Err(err).Str("span_id", spanID).Msg("trace stream: write heartbeat failed")
				return
			}
			flusher.Flush()
		case step, ok := <-ch:
			if !ok {
				// Run ended and the hub closed the channel, emit a
				// terminal SSE event so the UI can mark the modal as
				// complete instead of treating the disconnect as an
				// error.
				_, _ = fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			if !send(step) {
				return
			}
		}
	}
}

// HandleTracePrompt serves GET /traces/{span_id}/prompt, the composed
// prompt that was sent to the AI CLI for this run. Stored gzipped in
// the traces row; the store decompresses on the fly. Returns 404 when
// no prompt was recorded (pre-009-migration spans). Wire shape is plain
// text/plain; the UI renders it in a code block.
func (h *Handler) HandleTracePrompt(w http.ResponseWriter, r *http.Request) {
	spanID := mux.Vars(r)["span_id"]
	prompt, err := h.events.PromptForSpan(spanID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if prompt == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(prompt))
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

// HandleGraph serves GET /graph, the current agent interaction graph.
// Nodes are seeded from the configured fleet (issue #151: "Nodes = agents")
// and any edge endpoints not already covered by the current config (e.g.
// agents removed from config but with recorded dispatch history).
func (h *Handler) HandleGraph(w http.ResponseWriter, r *http.Request) {
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	edges := h.events.ListEdgesForWorkspace(workspaceID)

	// Build a map of the last cron error status for each agent so idle
	// agents that last exited with an error are flagged in the response.
	lastErrorByAgent := make(map[string]struct{})
	if h.sched != nil {
		for _, as := range h.sched.AgentStatuses() {
			if fleet.NormalizeWorkspaceID(as.WorkspaceID) == workspaceID && as.LastStatus == "error" {
				lastErrorByAgent[as.Name] = struct{}{}
			}
		}
	}

	nodeStatus := func(name string) string {
		if h.events != nil && h.events.IsRunning(name) {
			return "running"
		}
		if _, ok := lastErrorByAgent[name]; ok {
			return "error"
		}
		return ""
	}

	seen := make(map[string]struct{})
	configuredEdges := make(map[string]graphEdge)
	if h.store != nil {
		if agents, err := h.store.ReadAgents(); err == nil {
			agentNames := make(map[string]struct{})
			for _, a := range agents {
				if fleet.NormalizeWorkspaceID(a.WorkspaceID) != workspaceID {
					continue
				}
				seen[a.Name] = struct{}{}
				agentNames[a.Name] = struct{}{}
			}
			for _, a := range agents {
				if fleet.NormalizeWorkspaceID(a.WorkspaceID) != workspaceID {
					continue
				}
				for _, target := range a.CanDispatch {
					if _, ok := agentNames[target]; !ok {
						continue
					}
					key := graphEdgeKey(a.Name, target)
					configuredEdges[key] = graphEdge{From: a.Name, To: target, Dispatches: []dispatchRecord{}}
				}
			}
		} else {
			h.logger.Warn().Err(err).Msg("graph: read agents failed; node list will only include those with dispatch edges")
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
	slices.SortFunc(nodes, func(a, b graphNode) int { return cmp.Compare(a.ID, b.ID) })

	edgeByKey := make(map[string]graphEdge, len(edges)+len(configuredEdges))
	for key, e := range configuredEdges {
		edgeByKey[key] = e
	}
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
		edgeByKey[graphEdgeKey(e.From, e.To)] = graphEdge{
			From:       e.From,
			To:         e.To,
			Count:      e.Count,
			Dispatches: recs,
		}
	}
	wireEdges := make([]graphEdge, 0, len(edgeByKey))
	for _, e := range edgeByKey {
		if e.Dispatches == nil {
			e.Dispatches = []dispatchRecord{}
		}
		wireEdges = append(wireEdges, e)
	}
	slices.SortFunc(wireEdges, func(a, b graphEdge) int {
		if byFrom := cmp.Compare(a.From, b.From); byFrom != 0 {
			return byFrom
		}
		return cmp.Compare(a.To, b.To)
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(graphJSON{Nodes: nodes, Edges: wireEdges})
}

func graphEdgeKey(from, to string) string {
	return from + "\x00" + to
}

// ── /memory ────────────────────────────────────────────────────────────────

// HandleMemory serves GET /memory/{agent}/{repo}, returns the raw markdown
// content of the agent's memory for the given repo. The {repo} path segment
// is expected in the format "owner_repo" (underscore separator), matching
// both the filesystem layout and the normalised key in the SQLite memory
// store. The optional workspace query parameter defaults to Default. Returns
// 503 when no memory reader is configured.
func (h *Handler) HandleMemory(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agent := filepath.Clean(vars["agent"])
	repo := filepath.Clean(vars["repo"])
	workspace := r.URL.Query().Get("workspace")

	// Reject path traversal attempts.
	if agent == "." || repo == "." || agent == ".." || repo == ".." {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if h.memReader == nil {
		http.Error(w, "memory reader not configured", http.StatusServiceUnavailable)
		return
	}

	content, mtime, err := h.memReader.ReadMemory(workspace, agent, repo)
	if errors.Is(err, store.ErrMemoryNotFound) {
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
	serveSSE(w, r, h.events.MemorySSE)
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

func serveWorkspaceSSE(w http.ResponseWriter, r *http.Request, hub SSEHub) {
	raw, ok := r.URL.Query()["workspace"]
	if !ok {
		serveSSE(w, r, hub)
		return
	}
	workspaceID := fleet.NormalizeWorkspaceID("")
	if len(raw) > 0 {
		workspaceID = fleet.NormalizeWorkspaceID(raw[0])
	}
	ServeSSEWithIntervalFiltered(w, r, hub, defaultSSEHeartbeatInterval, func(msg []byte) bool {
		return workspaceFromSSEMessage(msg) == workspaceID
	})
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
	ServeSSEWithIntervalFiltered(w, r, hub, heartbeatInterval, nil)
}

// ServeSSEWithIntervalFiltered is ServeSSEWithInterval with an optional
// per-message predicate. Heartbeats and connection setup comments are never
// filtered.
func ServeSSEWithIntervalFiltered(w http.ResponseWriter, r *http.Request, hub SSEHub, heartbeatInterval time.Duration, filter func([]byte) bool) {
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
			if filter != nil && !filter(msg) {
				continue
			}
			_, err := w.Write(msg)
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func workspaceFromSSEMessage(msg []byte) string {
	for _, line := range bytes.Split(msg, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		var payload struct {
			WorkspaceID string `json:"workspace_id"`
			Workspace   string `json:"workspace"`
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if err := json.Unmarshal(data, &payload); err != nil {
			return ""
		}
		if payload.WorkspaceID != "" {
			return fleet.NormalizeWorkspaceID(payload.WorkspaceID)
		}
		if payload.Workspace != "" {
			return fleet.NormalizeWorkspaceID(payload.Workspace)
		}
		return ""
	}
	return ""
}
