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
//   - create_agent, delete_agent                          — agent CRUD writes
//   - create_skill, delete_skill                          — skill CRUD writes
//   - create_backend, delete_backend                      — backend CRUD writes
//   - create_repo, delete_repo                            — repo CRUD writes
//
// With repo CRUD in place this surface now covers the full fleet inventory
// declared in #227.
package mcp

import (
	"context"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
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

// ObserveStore is the read subset of observe.Store consumed by the
// observability tools (list_events, list_traces, get_trace, get_trace_steps,
// get_graph). It is satisfied by *observe.Store in production and by test
// stubs that return fixed data.
type ObserveStore interface {
	ListEvents(since time.Time) []observe.TimestampedEvent
	ListTraces() []observe.Span
	TracesByRootEventID(id string) []observe.Span
	ListSteps(spanID string) []workflow.TraceStep
	ListEdges() []observe.Edge
}

// DispatchStatsSource returns a snapshot of the dispatch counters.
// *workflow.Engine (and the same provider the REST /dispatches handler uses)
// satisfies this interface.
type DispatchStatsSource interface {
	DispatchStats() workflow.DispatchStats
}

// MemoryReader retrieves the stored memory for an (agent, repo) pair for the
// get_memory tool. The found flag lets the tool return a specific "memory not
// found" error without leaking webhook package sentinels into this package.
type MemoryReader interface {
	ReadMemory(agent, repo string) (content string, mtime time.Time, found bool, err error)
}

// ConfigReader returns the two read-only config snapshots the MCP tools
// expose: the redacted JSON used by get_config and the YAML fragment served by
// export_config. Both surfaces produce bytes matching the REST /config and
// /export responses so clients can rely on a single wire contract.
type ConfigReader interface {
	ConfigJSON() ([]byte, error)
	ExportYAML() ([]byte, error)
}

// ConfigImporter writes a YAML fragment (matching the export_config / GET
// /export shape) into the store. mode is empty/"merge" (upsert-only) or
// "replace" (prune entries not in the payload). Returns the per-section
// counts of imported entities — the same map handleStoreImport ships as JSON.
//
// Implementations must hold the store mutex while writing and reload cron
// schedules afterwards so MCP imports stay consistent with the REST path.
type ConfigImporter interface {
	ImportYAML(body []byte, mode string) (map[string]int, error)
}

// AgentWriter writes a single agent definition into the store and removes
// existing ones. Returns the canonical (normalized) form the store persisted
// so callers can show the same shape REST clients see in the POST /agents
// response.
//
// Implementations must hold the store mutex while writing and reload cron
// schedules afterwards so MCP writes stay consistent with the REST path.
type AgentWriter interface {
	UpsertAgent(a config.AgentDef) (config.AgentDef, error)
	DeleteAgent(name string, cascade bool) error
}

// SkillWriter writes a single skill into the store and removes existing ones.
// Upsert returns the canonical (normalized) name and SkillDef that were
// persisted so callers can surface the same shape REST clients see in the
// POST /skills response — lowercase name, trimmed prompt.
//
// Implementations must hold the store mutex while writing and reload cron
// schedules afterwards so MCP writes stay consistent with the REST path.
type SkillWriter interface {
	UpsertSkill(name string, sk config.SkillDef) (string, config.SkillDef, error)
	DeleteSkill(name string) error
}

// BackendWriter writes a single AI backend definition into the store and
// removes existing ones. Upsert returns the canonical (normalized) name and
// AIBackendConfig that were persisted so callers can surface the same shape
// REST clients see in the POST /backends response — lowercase name, trimmed
// command, defaults applied.
//
// Implementations must hold the store mutex while writing and reload cron
// schedules afterwards so MCP writes stay consistent with the REST path.
type BackendWriter interface {
	UpsertBackend(name string, b config.AIBackendConfig) (string, config.AIBackendConfig, error)
	DeleteBackend(name string) error
}

// RepoWriter writes a single repo definition (name, enabled flag, bindings)
// into the store and removes existing ones. Upsert returns the canonical
// (normalized) RepoDef that was persisted so callers can surface the same
// shape REST clients see in the POST /repos response — lowercase repo name,
// lowercased binding agents, trimmed cron, lowercased events.
//
// Implementations must hold the store mutex while writing and reload cron
// schedules afterwards so MCP writes stay consistent with the REST path.
type RepoWriter interface {
	UpsertRepo(r config.RepoDef) (config.RepoDef, error)
	DeleteRepo(name string) error
}

// Deps bundles the dependencies the MCP server needs. Each tool handler
// depends on a small subset of this struct; bundling them keeps the
// registration site in tools.go short.
//
// Config, Queue, Status, and Logger are always required. Observe,
// DispatchStats, Memory, ConfigBytes, ConfigImport, AgentWrite, SkillWrite,
// BackendWrite, and RepoWrite are optional: the observability,
// config-read/write, and CRUD-write tools are only registered when the
// corresponding dependency is supplied, so tests can exercise the core fleet
// surface without wiring the full stack.
type Deps struct {
	Config        ConfigProvider
	Queue         EventQueue
	Status        StatusSource
	Observe       ObserveStore
	DispatchStats DispatchStatsSource
	Memory        MemoryReader
	ConfigBytes   ConfigReader
	ConfigImport  ConfigImporter
	AgentWrite    AgentWriter
	SkillWrite    SkillWriter
	BackendWrite  BackendWriter
	RepoWrite     RepoWriter
	Logger        zerolog.Logger
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
delete_skill, create_backend, delete_backend, create_repo, delete_repo)
mutate the fleet through the same code path as the REST API. This
server is the v3 foundation for conversational fleet management.`
