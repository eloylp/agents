package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/workflow"
)

// ── /api/agents ────────────────────────────────────────────────────────────

// agentScheduleJSON carries scheduling state for cron-backed agents.
type agentScheduleJSON struct {
	LastRun    *string `json:"last_run,omitempty"` // RFC3339 or omitted
	NextRun    string  `json:"next_run"`           // RFC3339
	LastStatus string  `json:"last_status,omitempty"`
}

// agentBindingJSON is the wire shape for one agent-to-repo binding.
// Schedule is populated only for cron bindings that have scheduling state.
type agentBindingJSON struct {
	Repo     string             `json:"repo"`
	Labels   []string           `json:"labels,omitempty"`
	Events   []string           `json:"events,omitempty"`
	Cron     string             `json:"cron,omitempty"`
	Enabled  bool               `json:"enabled"`
	Schedule *agentScheduleJSON `json:"schedule,omitempty"`
}

// apiAgentJSON is the wire shape for one agent in /api/agents.
type apiAgentJSON struct {
	Name          string             `json:"name"`
	Backend       string             `json:"backend"`
	Skills        []string           `json:"skills,omitempty"`
	Description   string             `json:"description,omitempty"`
	AllowDispatch bool               `json:"allow_dispatch"`
	CanDispatch   []string           `json:"can_dispatch,omitempty"`
	AllowPRs      bool               `json:"allow_prs"`
	CurrentStatus string             `json:"current_status"` // "running" | "idle"
	Bindings      []agentBindingJSON `json:"bindings,omitempty"`
}

// handleAPIAgents serves GET /api/agents — a fleet snapshot combining agent
// definitions from config with scheduling state from the StatusProvider.
func (s *Server) handleAPIAgents(w http.ResponseWriter, _ *http.Request) {
	// Index scheduling state by (agent, repo) for O(1) lookup below.
	scheduleByKey := map[string]AgentStatus{}
	if s.provider != nil {
		for _, st := range s.provider.AgentStatuses() {
			scheduleByKey[st.Name+"\x00"+st.Repo] = st
		}
	}

	cfg := s.loadCfg()

	// Build one entry per configured agent.
	agents := make([]apiAgentJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		currentStatus := "idle"
		if s.runtimeState != nil && s.runtimeState.IsRunning(a.Name) {
			currentStatus = "running"
		}
		entry := apiAgentJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Skills:        a.Skills,
			Description:   a.Description,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
			AllowPRs:      a.AllowPRs,
			CurrentStatus: currentStatus,
		}

		// Collect bindings from all repos that reference this agent.
		// Disabled repos are excluded entirely — they are not active in the
		// runtime, so they should not appear in the fleet snapshot.
		for _, repo := range cfg.Repos {
			if !repo.Enabled {
				continue
			}
			for _, b := range repo.Use {
				if b.Agent != a.Name {
					continue
				}
				binding := agentBindingJSON{
					Repo:    repo.Name,
					Labels:  b.Labels,
					Events:  b.Events,
					Cron:    b.Cron,
					Enabled: b.IsEnabled(),
				}
				// Attach scheduling state onto the binding so agents with cron
				// schedules in multiple repos each carry their own schedule data.
				if b.IsCron() {
					if st, ok := scheduleByKey[a.Name+"\x00"+repo.Name]; ok {
						j := &agentScheduleJSON{
							NextRun:    st.NextRun.UTC().Format("2006-01-02T15:04:05Z"),
							LastStatus: st.LastStatus,
						}
						if st.LastRun != nil {
							lr := st.LastRun.UTC().Format("2006-01-02T15:04:05Z")
							j.LastRun = &lr
						}
						binding.Schedule = j
					}
				}
				entry.Bindings = append(entry.Bindings, binding)
			}
		}

		agents = append(agents, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agents)
}

// ── /api/config ────────────────────────────────────────────────────────────

// apiConfigJSON is the wire shape for /api/config with secrets redacted.
// Secrets (resolved values of *_env fields) are replaced with "[redacted]".
type apiConfigJSON struct {
	Daemon   apiDaemonJSON              `json:"daemon"`
	Skills   map[string]apiSkillJSON    `json:"skills,omitempty"`
	Agents   []apiAgentConfigJSON       `json:"agents,omitempty"`
	Repos    []apiRepoConfigJSON        `json:"repos,omitempty"`
}

type apiDaemonJSON struct {
	Log        apiLogConfigJSON                   `json:"log"`
	HTTP       apiHTTPConfigJSON                  `json:"http"`
	Processor  apiProcessorConfigJSON             `json:"processor"`
	MemoryDir  string                             `json:"memory_dir,omitempty"`
	AIBackends map[string]apiAIBackendConfigJSON  `json:"ai_backends,omitempty"`
	Proxy      apiProxyConfigJSON                 `json:"proxy"`
}

type apiLogConfigJSON struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type apiDispatchConfigJSON struct {
	MaxDepth           int `json:"max_depth"`
	MaxFanout          int `json:"max_fanout"`
	DedupWindowSeconds int `json:"dedup_window_seconds"`
}

type apiProcessorConfigJSON struct {
	EventQueueBuffer    int                   `json:"event_queue_buffer"`
	MaxConcurrentAgents int                   `json:"max_concurrent_agents"`
	Dispatch            apiDispatchConfigJSON  `json:"dispatch"`
}

// apiBindingConfigJSON is the wire shape for a repo binding in /api/config.
// Enabled is always an explicit bool: a nil *bool in config (meaning "default
// enabled") is normalized to true so clients see the effective value.
type apiBindingConfigJSON struct {
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Events  []string `json:"events,omitempty"`
	Enabled bool     `json:"enabled"`
}

// apiRepoConfigJSON is the wire shape for one repo in /api/config.
type apiRepoConfigJSON struct {
	Name    string                 `json:"name"`
	Enabled bool                   `json:"enabled"`
	Use     []apiBindingConfigJSON `json:"use,omitempty"`
}

type apiHTTPConfigJSON struct {
	ListenAddr             string `json:"listen_addr"`
	StatusPath             string `json:"status_path"`
	WebhookPath            string `json:"webhook_path"`
	AgentsRunPath          string `json:"agents_run_path"`
	WebhookSecretEnv       string `json:"webhook_secret_env,omitempty"`
	WebhookSecret          string `json:"webhook_secret,omitempty"` // always "[redacted]" when set
	APIKeyEnv              string `json:"api_key_env,omitempty"`
	APIKey                 string `json:"api_key,omitempty"` // always "[redacted]" when set
	ReadTimeoutSeconds     int    `json:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `json:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `json:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `json:"max_body_bytes"`
	DeliveryTTLSeconds     int    `json:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
}

type apiAIBackendConfigJSON struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"` // values are "[redacted]"
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MaxPromptChars   int               `json:"max_prompt_chars"`
	RedactionSaltEnv string            `json:"redaction_salt_env,omitempty"`
}

type apiProxyConfigJSON struct {
	Enabled  bool                   `json:"enabled"`
	Path     string                 `json:"path,omitempty"`
	Upstream apiProxyUpstreamJSON   `json:"upstream,omitempty"`
}

type apiProxyUpstreamJSON struct {
	URL            string `json:"url,omitempty"`
	Model          string `json:"model,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	APIKey         string `json:"api_key,omitempty"` // always "[redacted]" when set
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// ExtraBody is intentionally omitted: values can contain bearer tokens or
	// other secrets and there is no way to safely distinguish them from safe
	// tuning knobs without domain knowledge of every possible upstream vendor.
}

type apiSkillJSON struct {
	PromptFile string `json:"prompt_file,omitempty"`
	// Prompt body is intentionally omitted: it can be very long.
}

type apiAgentConfigJSON struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	PromptFile    string   `json:"prompt_file,omitempty"`
	Description   string   `json:"description,omitempty"`
	AllowPRs      bool     `json:"allow_prs"`
	AllowDispatch bool     `json:"allow_dispatch"`
	CanDispatch   []string `json:"can_dispatch,omitempty"`
}

const redacted = "[redacted]"

// handleAPIConfig serves GET /api/config — the effective parsed config with
// secret values replaced by "[redacted]". Env-var names are preserved so
// operators can identify which environment variable holds a given secret.
func (s *Server) handleAPIConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.loadCfg()

	httpCfg := apiHTTPConfigJSON{
		ListenAddr:             cfg.Daemon.HTTP.ListenAddr,
		StatusPath:             cfg.Daemon.HTTP.StatusPath,
		WebhookPath:            cfg.Daemon.HTTP.WebhookPath,
		AgentsRunPath:          cfg.Daemon.HTTP.AgentsRunPath,
		WebhookSecretEnv:       cfg.Daemon.HTTP.WebhookSecretEnv,
		APIKeyEnv:              cfg.Daemon.HTTP.APIKeyEnv,
		ReadTimeoutSeconds:     cfg.Daemon.HTTP.ReadTimeoutSeconds,
		WriteTimeoutSeconds:    cfg.Daemon.HTTP.WriteTimeoutSeconds,
		IdleTimeoutSeconds:     cfg.Daemon.HTTP.IdleTimeoutSeconds,
		MaxBodyBytes:           cfg.Daemon.HTTP.MaxBodyBytes,
		DeliveryTTLSeconds:     cfg.Daemon.HTTP.DeliveryTTLSeconds,
		ShutdownTimeoutSeconds: cfg.Daemon.HTTP.ShutdownTimeoutSeconds,
	}
	if cfg.Daemon.HTTP.WebhookSecret != "" {
		httpCfg.WebhookSecret = redacted
	}
	if cfg.Daemon.HTTP.APIKey != "" {
		httpCfg.APIKey = redacted
	}

	backends := make(map[string]apiAIBackendConfigJSON, len(cfg.Daemon.AIBackends))
	for name, b := range cfg.Daemon.AIBackends {
		redactedEnv := make(map[string]string, len(b.Env))
		for k := range b.Env {
			redactedEnv[k] = redacted
		}
		backends[name] = apiAIBackendConfigJSON{
			Command:          b.Command,
			Args:             b.Args,
			Env:              redactedEnv,
			TimeoutSeconds:   b.TimeoutSeconds,
			MaxPromptChars:   b.MaxPromptChars,
			RedactionSaltEnv: b.RedactionSaltEnv,
		}
	}

	proxy := apiProxyConfigJSON{
		Enabled: cfg.Daemon.Proxy.Enabled,
		Path:    cfg.Daemon.Proxy.Path,
		Upstream: apiProxyUpstreamJSON{
			URL:            cfg.Daemon.Proxy.Upstream.URL,
			Model:          cfg.Daemon.Proxy.Upstream.Model,
			APIKeyEnv:      cfg.Daemon.Proxy.Upstream.APIKeyEnv,
			TimeoutSeconds: cfg.Daemon.Proxy.Upstream.TimeoutSeconds,
			// ExtraBody is not copied: see apiProxyUpstreamJSON comment.
		},
	}
	if cfg.Daemon.Proxy.Upstream.APIKey != "" {
		proxy.Upstream.APIKey = redacted
	}

	skills := make(map[string]apiSkillJSON, len(cfg.Skills))
	for name, skill := range cfg.Skills {
		skills[name] = apiSkillJSON{PromptFile: skill.PromptFile}
	}

	agents := make([]apiAgentConfigJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents = append(agents, apiAgentConfigJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Skills:        a.Skills,
			PromptFile:    a.PromptFile,
			Description:   a.Description,
			AllowPRs:      a.AllowPRs,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
		})
	}

	repos := make([]apiRepoConfigJSON, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		bindings := make([]apiBindingConfigJSON, 0, len(r.Use))
		for _, b := range r.Use {
			bindings = append(bindings, apiBindingConfigJSON{
				Agent:   b.Agent,
				Labels:  b.Labels,
				Cron:    b.Cron,
				Events:  b.Events,
				Enabled: b.IsEnabled(),
			})
		}
		repos = append(repos, apiRepoConfigJSON{
			Name:    r.Name,
			Enabled: r.Enabled,
			Use:     bindings,
		})
	}

	resp := apiConfigJSON{
		Daemon: apiDaemonJSON{
			Log: apiLogConfigJSON{
				Level:  cfg.Daemon.Log.Level,
				Format: cfg.Daemon.Log.Format,
			},
			HTTP: httpCfg,
			Processor: apiProcessorConfigJSON{
				EventQueueBuffer:    cfg.Daemon.Processor.EventQueueBuffer,
				MaxConcurrentAgents: cfg.Daemon.Processor.MaxConcurrentAgents,
				Dispatch: apiDispatchConfigJSON{
					MaxDepth:           cfg.Daemon.Processor.Dispatch.MaxDepth,
					MaxFanout:          cfg.Daemon.Processor.Dispatch.MaxFanout,
					DedupWindowSeconds: cfg.Daemon.Processor.Dispatch.DedupWindowSeconds,
				},
			},
			MemoryDir:  cfg.Daemon.MemoryDir,
			AIBackends: backends,
			Proxy:      proxy,
		},
		Skills: skills,
		Agents: agents,
		Repos:  repos,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ── /api/dispatches ────────────────────────────────────────────────────────

// handleAPIDispatches serves GET /api/dispatches — the current dispatch
// counters as reported by the DispatchStatsProvider. Returns an empty object
// when no provider is configured (e.g. no dispatch configured).
func (s *Server) handleAPIDispatches(w http.ResponseWriter, _ *http.Request) {
	var stats workflow.DispatchStats
	if s.dispatchStats != nil {
		stats = s.dispatchStats.DispatchStats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// ── /api/events ────────────────────────────────────────────────────────────

// apiEventJSON is the wire shape for one event in /api/events.
type apiEventJSON struct {
	At      string         `json:"at"`
	ID      string         `json:"id"`
	Repo    string         `json:"repo"`
	Kind    string         `json:"kind"`
	Number  int            `json:"number"`
	Actor   string         `json:"actor"`
	Payload map[string]any `json:"payload,omitempty"`
}

// handleAPIEvents serves GET /api/events — recent event history.
// An optional ?since=<RFC3339> query parameter filters to events after that time.
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			since = t
		}
	}

	events := s.observeStore.Events.List(since)
	out := make([]apiEventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, apiEventJSON{
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

// handleAPIEventsStream serves GET /api/events/stream as a Server-Sent Events
// stream. Each new event is pushed as a "data: <json>\n\n" message.
func (s *Server) handleAPIEventsStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, s.observeStore.EventsSSE)
}

// ── /api/traces ────────────────────────────────────────────────────────────

// handleAPITraces serves GET /api/traces — the most recent agent run spans.
func (s *Server) handleAPITraces(w http.ResponseWriter, _ *http.Request) {
	spans := s.observeStore.Traces.List()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spans)
}

// handleAPITrace serves GET /api/traces/{root_event_id} — all spans for one
// root event.
func (s *Server) handleAPITrace(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["root_event_id"]
	spans := s.observeStore.Traces.ByRootEventID(id)
	if len(spans) == 0 {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spans)
}

// handleAPITracesStream serves GET /api/traces/stream as a Server-Sent Events
// stream. Each completed span is pushed as a "data: <json>\n\n" message.
func (s *Server) handleAPITracesStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, s.observeStore.TracesSSE)
}

// ── /api/graph ─────────────────────────────────────────────────────────────

// apiGraphJSON is the wire shape for GET /api/graph.
type apiGraphJSON struct {
	Nodes []apiGraphNode `json:"nodes"`
	Edges []apiGraphEdge `json:"edges"`
}

type apiGraphNode struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"` // last known run status; empty when not yet run
}

type apiGraphEdge struct {
	From       string              `json:"from"`
	To         string              `json:"to"`
	Count      int                 `json:"count"`
	Dispatches []apiDispatchRecord `json:"dispatches"`
}

type apiDispatchRecord struct {
	At     string `json:"at"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Reason string `json:"reason"`
}

// handleAPIGraph serves GET /api/graph — the current agent interaction graph.
func (s *Server) handleAPIGraph(w http.ResponseWriter, _ *http.Request) {
	edges := s.observeStore.Graph.Edges()

	// Build a map of the last cron error status for each agent so we can show
	// "error" for idle agents that last exited with an error.
	lastErrorByAgent := make(map[string]bool)
	if s.provider != nil {
		for _, as := range s.provider.AgentStatuses() {
			if as.LastStatus == "error" {
				lastErrorByAgent[as.Name] = true
			}
		}
	}

	// Derive the display status for a node: "running" if currently active,
	// "error" if idle but last run failed, otherwise omit (UI treats it as idle).
	nodeStatus := func(name string) string {
		if s.runtimeState != nil && s.runtimeState.IsRunning(name) {
			return "running"
		}
		if lastErrorByAgent[name] {
			return "error"
		}
		return ""
	}

	// Seed the node set from the full configured fleet so that agents with no
	// dispatch history still appear in the graph (issue #151: "Nodes = agents").
	seen := make(map[string]struct{})
	for _, a := range s.loadCfg().Agents {
		seen[a.Name] = struct{}{}
	}
	// Include any edge endpoints not already covered by the current config
	// (e.g. agents removed from config but with recorded dispatch history).
	for _, e := range edges {
		seen[e.From] = struct{}{}
		seen[e.To] = struct{}{}
	}
	nodes := make([]apiGraphNode, 0, len(seen))
	for id := range seen {
		nodes = append(nodes, apiGraphNode{ID: id, Status: nodeStatus(id)})
	}

	wireEdges := make([]apiGraphEdge, 0, len(edges))
	for _, e := range edges {
		recs := make([]apiDispatchRecord, 0, len(e.Dispatches))
		for _, d := range e.Dispatches {
			recs = append(recs, apiDispatchRecord{
				At:     d.At.UTC().Format(time.RFC3339),
				Repo:   d.Repo,
				Number: d.Number,
				Reason: d.Reason,
			})
		}
		wireEdges = append(wireEdges, apiGraphEdge{
			From:       e.From,
			To:         e.To,
			Count:      e.Count,
			Dispatches: recs,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apiGraphJSON{Nodes: nodes, Edges: wireEdges})
}

// ── /api/memory ────────────────────────────────────────────────────────────

// handleAPIMemory serves GET /api/memory/{agent}/{repo} — returns the raw
// markdown content of the agent's memory for the given repo.
// The {repo} path segment is expected in the format "owner_repo" (underscore
// separator, matching both the filesystem layout under memory_dir and the
// normalised key used in the SQLite memory store).
func (s *Server) handleAPIMemory(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	agent := filepath.Clean(vars["agent"])
	repo := filepath.Clean(vars["repo"])

	// Reject path traversal attempts.
	if agent == "." || repo == "." || agent == ".." || repo == ".." {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// SQLite mode: read memory via the injected MemoryReader.
	if s.memReader != nil {
		content, err := s.memReader.ReadMemory(agent, repo)
		if errors.Is(err, ErrMemoryNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "could not read memory", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(content))
		return
	}

	// File mode: read from the filesystem memory_dir.
	memDir := s.loadCfg().Daemon.MemoryDir
	if memDir == "" {
		http.Error(w, "memory_dir not configured", http.StatusNotFound)
		return
	}

	// Build the candidate path as an absolute, cleaned path and verify it
	// stays within memDir.  filepath.Join calls filepath.Clean internally so
	// any remaining "../.." sequences are resolved before the prefix check.
	root, err := filepath.Abs(memDir)
	if err != nil {
		http.Error(w, "invalid memory_dir", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(root, agent, repo, "MEMORY.md")
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "memory file not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not stat memory file", http.StatusInternalServerError)
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "could not read memory file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Memory-Mtime", info.ModTime().UTC().Format(time.RFC3339))
	_, _ = w.Write(data)
}

// handleAPIMemoryStream serves GET /api/memory/stream as a Server-Sent Events
// stream that notifies subscribers when any memory file changes.
func (s *Server) handleAPIMemoryStream(w http.ResponseWriter, r *http.Request) {
	serveSSE(w, r, s.observeStore.MemorySSE)
}

// ── SSE helper ─────────────────────────────────────────────────────────────

// defaultSSEHeartbeatInterval is how often serveSSE writes a comment to keep
// the TCP connection alive through intermediate proxies.
const defaultSSEHeartbeatInterval = 30 * time.Second

// serveSSE subscribes the current HTTP connection to hub, streams incoming
// messages, and unsubscribes on client disconnect or context cancellation.
// A periodic comment heartbeat (": heartbeat\n\n") is written every 30 s to
// keep the connection alive through proxies that close idle TCP connections
// (e.g. nginx's proxy_read_timeout).
func serveSSE(w http.ResponseWriter, r *http.Request, hub interface {
	Subscribe() chan []byte
	Unsubscribe(chan []byte)
}) {
	serveSSEWithInterval(w, r, hub, defaultSSEHeartbeatInterval)
}

// serveSSEWithInterval is the testable core of serveSSE; callers that need a
// different heartbeat period (e.g. tests) use this directly.
func serveSSEWithInterval(w http.ResponseWriter, r *http.Request, hub interface {
	Subscribe() chan []byte
	Unsubscribe(chan []byte)
}, heartbeatInterval time.Duration) {
	// Clear the per-connection write deadline that http.Server.WriteTimeout
	// installs. Non-SSE routes are protected by that deadline (and additionally
	// by http.TimeoutHandler); SSE streams must be allowed to write indefinitely,
	// so we remove the deadline here without affecting other connections.
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
