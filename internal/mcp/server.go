// Package mcp implements the Model Context Protocol (MCP) server that exposes
// fleet management operations as tools over Streamable HTTP transport.
//
// MCP clients (Claude Code, Cursor, Cline, ...) register the daemon at /mcp
// and then discover the available tools automatically. This is the v3
// foundation for conversational fleet management — tracked in issue #227.
//
// The current tool inventory covers fleet reads, on-demand runs, the
// read-only observability surface, and config snapshots / import:
//
//   - list_agents, list_skills, list_backends, list_repos — fleet lists
//   - get_agent, get_skill, get_backend, get_repo         — per-item reads
//   - get_status                                          — health snapshot
//   - trigger_agent                                       — on-demand run
//   - list_events, list_traces, get_trace, get_trace_steps — agent activity
//   - get_graph, get_dispatches, get_memory               — dispatch + memory
//   - get_config, export_config, import_config            — config snapshots / write
//   - create_agent, update_agent, delete_agent            — agent CRUD writes
//   - create_skill, update_skill, delete_skill            — skill CRUD writes
//   - create_backend, update_backend, delete_backend      — backend CRUD writes
//   - create_repo, delete_repo                            — repo CRUD writes
//   - create_binding, get_binding, update_binding, delete_binding — atomic binding CRUD
//
// With repo CRUD in place this surface now covers the full fleet inventory
// declared in #227.
package mcp

import (
	"database/sql"
	"net/http"

	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	internalserver "github.com/eloylp/agents/internal/server"
	serverconfig "github.com/eloylp/agents/internal/server/config"
	serverfleet "github.com/eloylp/agents/internal/server/fleet"
	serverrepos "github.com/eloylp/agents/internal/server/repos"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/workflow"
)

// Version is advertised to MCP clients during the initialize handshake.
// It tracks the wire contract (tool names, shapes) — bump it whenever a
// tool's input or output schema changes in a non-backwards-compatible way.
const Version = "0.1.0"

// Deps bundles the dependencies the MCP tools call into. The composing
// daemon (cmd/agents) constructs each component once and hands the same
// references to the REST and MCP surfaces so both stay in lock-step.
//
// Server, Queue, and Logger are always required. Observe, Engine, Fleet,
// Repos, and Config are optional: tools that depend on them are only
// registered when the field is non-nil, so a minimal MCP server can serve
// the core fleet + status + trigger surface without wiring observability
// or CRUD writes.
type Deps struct {
	DB      *sql.DB
	Server  *internalserver.Server  // Config() snapshot, StatusJSON()
	Queue   *workflow.DataChannels  // PushEvent for trigger_agent
	Observe *observe.Store          // observability tools (events, traces, graph)
	Engine  *workflow.Engine        // DispatchStats() for get_dispatches
	Fleet   *serverfleet.Handler    // agent / skill / backend CRUD writes
	Repos   *serverrepos.Handler    // repo + binding CRUD writes
	Config  *serverconfig.Handler   // ConfigJSON / ExportYAML / ImportYAML
	Logger  zerolog.Logger
}

// Handler is an http.Handler that speaks MCP over Streamable HTTP. Mount it
// at /mcp on the daemon's HTTP server.
type Handler struct {
	mcpSrv *server.MCPServer
	http   http.Handler
}

// New constructs an MCP handler with the core tool set registered.
func New(deps Deps) *Handler {
	mcpSrv := server.NewMCPServer(
		"agents",
		Version,
		server.WithToolCapabilities(true),
		server.WithInstructions(serverInstructions),
	)
	registerTools(mcpSrv, deps)
	return &Handler{
		mcpSrv: mcpSrv,
		http:   server.NewStreamableHTTPServer(mcpSrv),
	}
}

// ServeHTTP dispatches MCP requests to the underlying streamable HTTP server.
// The mcp-go library interprets the request method (POST/GET/DELETE) — the
// mount path can be anything, including the canonical /mcp used by the
// daemon.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.http.ServeHTTP(w, r)
}

// serverInstructions is shown to MCP clients during the initialize handshake
// so the connected model understands the domain without a custom system
// prompt. Keep it short — the tool descriptions carry the per-call details.
const serverInstructions = `This MCP server exposes fleet management tools for the agents daemon.

Domain model:
  - agent   — a capability definition (backend, model, skills, prompt).
  - skill   — a reusable prompt fragment that agents can compose.
  - backend — an AI CLI runner (claude, codex, or a named local backend).
  - repo    — a GitHub repo with bindings that wire agents to triggers
              (labels, events, or cron).

Use list_* tools to enumerate the fleet and get_* tools to drill into a
single agent, skill, backend, repo, trace, or memory record.
get_status returns daemon health. trigger_agent fires an on-demand run.
Observability tools (list_events, list_traces, get_trace,
get_trace_steps, get_graph, get_dispatches, get_memory) expose the same
data the web dashboard shows. Config tools (get_config, export_config,
import_config) return the redacted effective config, export the
CRUD-mutable YAML fragment, and write a YAML payload back into the
store. CRUD write tools (create_agent, delete_agent, create_skill,
delete_skill, create_backend, delete_backend, create_repo, delete_repo,
create_binding, update_binding, delete_binding) mutate the fleet through
the same code path as the REST API. This server is the v3 foundation
for conversational fleet management.`
