// Package mcp implements the Model Context Protocol (MCP) server that exposes
// fleet management operations as tools over Streamable HTTP transport.
//
// MCP clients (Claude Code, Cursor, Cline, ...) register the daemon at /mcp
// and then discover the available tools automatically. This is the v3
// foundation for conversational fleet management — tracked in issue #227.
//
// This first cut ships a core subset of tools sufficient to demonstrate the
// architecture and make the endpoint useful:
//
//   - list_agents, list_skills, list_backends, list_repos — fleet reads
//   - get_status                                          — health snapshot
//   - trigger_agent                                       — on-demand run
//
// The remaining CRUD writes, observability queries, and config import/export
// tools are intentionally left for follow-up PRs so each batch stays
// reviewable.
package mcp

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// Version is advertised to MCP clients during the initialize handshake.
// It tracks the wire contract (tool names, shapes) — bump it whenever a
// tool's input or output schema changes in a non-backwards-compatible way.
const Version = "0.1.0"

// ConfigProvider supplies the current effective fleet configuration snapshot.
// *webhook.Server satisfies this interface via its Config() method so MCP
// tools observe the same in-memory config the REST API does, including any
// hot-reloaded state from CRUD writes.
type ConfigProvider interface {
	Config() *config.Config
}

// EventQueue enqueues workflow events. The trigger_agent tool uses it to
// fire on-demand agents.run events, matching the behaviour of POST /run.
// *workflow.DataChannels satisfies this interface.
type EventQueue interface {
	PushEvent(ctx context.Context, ev workflow.Event) error
}

// StatusSource returns the JSON-encoded /status payload. The get_status tool
// reuses the existing handler output so MCP callers see the identical shape
// the REST API exposes — no risk of drift between the two surfaces.
type StatusSource interface {
	StatusJSON() ([]byte, error)
}

// Deps bundles the dependencies the MCP server needs. Each tool handler
// depends on a small subset of this struct; bundling them keeps the
// registration site in tools.go short.
type Deps struct {
	Config ConfigProvider
	Queue  EventQueue
	Status StatusSource
	Logger zerolog.Logger
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

Use list_* tools to inspect the fleet, get_status for health, and
trigger_agent to fire an on-demand run. This server is the v3 foundation;
additional CRUD and observability tools will land in follow-up releases.`
