package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/config"
)

// registerTools wires the core tool set onto the MCP server. Handlers read
// config from deps.Config and enqueue events via deps.Queue, matching the
// semantics of the equivalent REST endpoints.
func registerTools(srv *server.MCPServer, deps Deps) {
	srv.AddTool(
		mcpgo.NewTool("list_agents",
			mcpgo.WithDescription("List every configured agent with backend, model, skills, and dispatch wiring. Returns the same shape POST /agents accepts."),
		),
		toolListAgents(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_agent",
			mcpgo.WithDescription("Fetch one agent's configuration by name. Returns the same shape as an element of list_agents."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Agent name (case-insensitive)."),
			),
		),
		toolGetAgent(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_skills",
			mcpgo.WithDescription("List every configured skill with its prompt body. Skills are reusable prompt fragments agents can compose."),
		),
		toolListSkills(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_skill",
			mcpgo.WithDescription("Fetch one skill's full prompt body by name."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Skill name (case-insensitive)."),
			),
		),
		toolGetSkill(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_backends",
			mcpgo.WithDescription("List every configured AI backend (command, models, timeouts). Includes local backends routed through the translation proxy."),
		),
		toolListBackends(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_backend",
			mcpgo.WithDescription("Fetch one AI backend's configuration and health state by name."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Backend name (case-insensitive)."),
			),
		),
		toolGetBackend(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_repos",
			mcpgo.WithDescription("List every configured repo and its agent bindings (labels, events, cron)."),
		),
		toolListRepos(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_repo",
			mcpgo.WithDescription("Fetch one repo's bindings and enabled state by full name."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Repo full name in owner/name form (case-insensitive)."),
			),
		),
		toolGetRepo(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_status",
			mcpgo.WithDescription("Daemon health snapshot: uptime, event queue depth, autonomous agent schedules, dispatch counters, orphaned-agent summary."),
		),
		toolGetStatus(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("trigger_agent",
			mcpgo.WithDescription("Trigger an on-demand agent run on a repo. Returns the event ID; the run is async."),
			mcpgo.WithString("agent",
				mcpgo.Required(),
				mcpgo.Description("Name of the agent to run (must exist in the fleet)."),
			),
			mcpgo.WithString("repo",
				mcpgo.Required(),
				mcpgo.Description("Repo in owner/name form (must be enabled in the fleet)."),
			),
		),
		toolTriggerAgent(deps),
	)
	if deps.Observe != nil {
		srv.AddTool(
			mcpgo.NewTool("list_events",
				mcpgo.WithDescription("List recent events (GitHub webhook deliveries, cron firings, on-demand runs, dispatches). Ordered oldest→newest, capped at 500."),
				mcpgo.WithString("since",
					mcpgo.Description("Optional RFC3339 timestamp; return only events strictly after this time."),
				),
			),
			toolListEvents(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("list_traces",
				mcpgo.WithDescription("List recent agent run spans. Ordered newest→oldest, capped at 200. Each span records backend, repo, status, duration, and summary."),
			),
			toolListTraces(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("get_trace",
				mcpgo.WithDescription("Fetch every span in a single trace by root event ID. Returns the full dispatch chain rooted at the originating event."),
				mcpgo.WithString("root_event_id",
					mcpgo.Required(),
					mcpgo.Description("Root event ID of the trace (e.g. from list_events or list_traces)."),
				),
			),
			toolGetTrace(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("get_trace_steps",
				mcpgo.WithDescription("Fetch the tool-loop transcript for one span: ordered tool calls with input/output summaries and durations."),
				mcpgo.WithString("span_id",
					mcpgo.Required(),
					mcpgo.Description("Span ID to fetch steps for."),
				),
			),
			toolGetTraceSteps(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("get_graph",
				mcpgo.WithDescription("Return the agent interaction graph: every configured agent as a node plus observed inter-agent dispatch edges with counts and individual dispatch records."),
			),
			toolGetGraph(deps),
		)
	}
	if deps.DispatchStats != nil {
		srv.AddTool(
			mcpgo.NewTool("get_dispatches",
				mcpgo.WithDescription("Return current dispatch counters: requested, enqueued, deduped, and drop reasons (no whitelist, no opt-in, self, depth, fanout)."),
			),
			toolGetDispatches(deps),
		)
	}
	if deps.Memory != nil {
		srv.AddTool(
			mcpgo.NewTool("get_memory",
				mcpgo.WithDescription("Return the stored markdown memory for an agent/repo pair. Only autonomous agents keep memory; event-driven runs don't."),
				mcpgo.WithString("agent",
					mcpgo.Required(),
					mcpgo.Description("Agent name."),
				),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo in owner/repo or owner_repo form."),
				),
			),
			toolGetMemory(deps),
		)
	}
	if deps.ConfigBytes != nil {
		srv.AddTool(
			mcpgo.NewTool("get_config",
				mcpgo.WithDescription("Return the effective parsed daemon config as JSON with secrets redacted. Same wire shape as GET /config."),
			),
			toolGetConfig(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("export_config",
				mcpgo.WithDescription("Return the CRUD-mutable fleet config (agents, skills, repos, ai_backends) as a YAML fragment. Same body as GET /export — suitable for piping back into POST /import."),
			),
			toolExportConfig(deps),
		)
	}
	if deps.ConfigImport != nil {
		srv.AddTool(
			mcpgo.NewTool("import_config",
				mcpgo.WithDescription("Write a YAML config fragment (agents, skills, repos, ai_backends) into the store. mode=\"\" or \"merge\" upserts; mode=\"replace\" prunes entries not in the payload. Returns per-section counts. Same path as POST /import."),
				mcpgo.WithString("yaml",
					mcpgo.Required(),
					mcpgo.Description("YAML body matching the export_config / GET /export shape."),
				),
				mcpgo.WithString("mode",
					mcpgo.Description("\"\" or \"merge\" to upsert, \"replace\" to prune entries not in the payload."),
				),
			),
			toolImportConfig(deps),
		)
	}
	if deps.SkillWrite != nil {
		srv.AddTool(
			mcpgo.NewTool("create_skill",
				mcpgo.WithDescription("Create or update a skill. Upsert semantics: a write to an existing name overwrites it. Returns the canonical (normalized) skill persisted by the store. Same path as POST /skills."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Skill name. Lowercased and trimmed by the store."),
				),
				mcpgo.WithString("prompt",
					mcpgo.Description("Skill prompt body (reusable guidance injected into composing agents' system prompt)."),
				),
			),
			toolCreateSkill(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_skill",
				mcpgo.WithDescription("Delete a skill by name. Fails with a conflict error if any agent still references the skill. Same path as DELETE /skills/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Skill name (case-insensitive; matched after lowercasing)."),
				),
			),
			toolDeleteSkill(deps),
		)
	}
	if deps.BackendWrite != nil {
		srv.AddTool(
			mcpgo.NewTool("create_backend",
				mcpgo.WithDescription("Create or update an AI backend. Upsert semantics: a write to an existing name overwrites it. Returns the canonical (normalized) backend persisted by the store. Same path as POST /backends."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Backend name. Lowercased and trimmed by the store. Built-in names: \"claude\", \"codex\". Any other name creates a named backend."),
				),
				mcpgo.WithString("command",
					mcpgo.Description("Path to the CLI binary the daemon invokes for this backend (e.g. \"claude\" or \"/usr/local/bin/codex\")."),
				),
				mcpgo.WithArray("models",
					mcpgo.Description("Optional model catalog for this backend. Agents pinning a model must name one that appears here."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithString("local_model_url",
					mcpgo.Description("Optional OpenAI-compatible base URL for local backends. Triggers ANTHROPIC_BASE_URL injection at runtime. Leave empty for upstream CLIs."),
				),
				mcpgo.WithNumber("timeout_seconds",
					mcpgo.Description("Per-run CLI timeout in seconds. Defaults are applied when zero."),
				),
				mcpgo.WithNumber("max_prompt_chars",
					mcpgo.Description("Maximum composed prompt length in characters. Defaults are applied when zero."),
				),
				mcpgo.WithString("redaction_salt_env",
					mcpgo.Description("Name of the environment variable carrying the prompt-log redaction salt for this backend."),
				),
			),
			toolCreateBackend(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_backend",
				mcpgo.WithDescription("Delete a backend by name. Fails with a conflict error if any agent still references the backend. Same path as DELETE /backends/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Backend name (case-insensitive; matched after lowercasing)."),
				),
			),
			toolDeleteBackend(deps),
		)
	}
	if deps.AgentWrite != nil {
		srv.AddTool(
			mcpgo.NewTool("create_agent",
				mcpgo.WithDescription("Create or update an agent. Upsert semantics: a write to an existing name overwrites it. Returns the canonical (normalized) agent persisted by the store. Same path as POST /agents."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Agent name. Lowercased and trimmed by the store."),
				),
				mcpgo.WithString("backend",
					mcpgo.Description("AI backend name (must exist in the fleet). Required for runnable agents."),
				),
				mcpgo.WithString("model",
					mcpgo.Description("Optional model identifier; must be present in the backend's model catalog."),
				),
				mcpgo.WithString("prompt",
					mcpgo.Description("Agent prompt body."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Short human-readable description shown in the dispatch roster."),
				),
				mcpgo.WithArray("skills",
					mcpgo.Description("Optional list of skill names to compose into the agent's system prompt."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithArray("can_dispatch",
					mcpgo.Description("Optional whitelist of agent names this agent may dispatch to."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithBoolean("allow_prs",
					mcpgo.Description("Allow this agent to open or edit pull requests. Defaults to false."),
				),
				mcpgo.WithBoolean("allow_dispatch",
					mcpgo.Description("Allow other agents to dispatch this agent. Defaults to false."),
				),
			),
			toolCreateAgent(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_agent",
				mcpgo.WithDescription("Delete an agent by name. Without cascade=true the call fails with a conflict error if any repo binding still references the agent. Same path as DELETE /agents/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Agent name (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithBoolean("cascade",
					mcpgo.Description("When true, also remove repo bindings that reference the agent. Defaults to false."),
				),
			),
			toolDeleteAgent(deps),
		)
	}
	if deps.RepoWrite != nil {
		srv.AddTool(
			mcpgo.NewTool("create_repo",
				mcpgo.WithDescription("Create or update a repo and its bindings. Upsert semantics: a write to an existing name overwrites it, replacing the bindings list. Returns the canonical (normalized) repo persisted by the store. Same path as POST /repos."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\". Lowercased and trimmed by the store."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("Whether the repo is active. Defaults to false — callers must opt in explicitly, matching POST /repos."),
				),
				mcpgo.WithArray("bindings",
					mcpgo.Description("Optional list of agent bindings on this repo. Each binding wires one agent to exactly one trigger: labels (array), events (array), or cron (string). An agent may appear multiple times with different triggers. Replacing a repo replaces the whole bindings list."),
					mcpgo.Items(map[string]any{
						"type": "object",
						"properties": map[string]any{
							"agent":   map[string]any{"type": "string", "description": "Agent name to bind."},
							"labels":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Label-triggered binding: fire when one of these labels is applied."},
							"events":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Event-triggered binding: fire on these GitHub event kinds (e.g. issues.opened)."},
							"cron":    map[string]any{"type": "string", "description": "Cron-triggered binding: schedule expression for autonomous runs."},
							"enabled": map[string]any{"type": "boolean", "description": "Whether this binding is active. Absent = enabled; only explicit false disables."},
						},
						"required": []any{"agent"},
					}),
				),
			),
			toolCreateRepo(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_repo",
				mcpgo.WithDescription("Delete a repo (and its bindings) by full name. Same path as DELETE /repos/{owner}/{repo}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" (case-insensitive; matched after lowercasing)."),
				),
			),
			toolDeleteRepo(deps),
		)
	}
}

// agentJSON converts a config.AgentDef to the snake_case map shape used by
// the REST API, so the MCP and HTTP surfaces stay aligned.
func agentJSON(a config.AgentDef) map[string]any {
	return map[string]any{
		"name":           a.Name,
		"backend":        a.Backend,
		"model":          a.Model,
		"skills":         nilSafe(a.Skills),
		"description":    a.Description,
		"allow_prs":      a.AllowPRs,
		"allow_dispatch": a.AllowDispatch,
		"can_dispatch":   nilSafe(a.CanDispatch),
	}
}

// backendJSON renders one AI backend entry in the snake_case shape shared
// between list_backends and get_backend.
func backendJSON(name string, b config.AIBackendConfig) map[string]any {
	return map[string]any{
		"name":               name,
		"command":            b.Command,
		"version":            b.Version,
		"models":             nilSafe(b.Models),
		"healthy":            b.Healthy,
		"health_detail":      b.HealthDetail,
		"local_model_url":    b.LocalModelURL,
		"timeout_seconds":    b.TimeoutSeconds,
		"max_prompt_chars":   b.MaxPromptChars,
		"redaction_salt_env": b.RedactionSaltEnv,
	}
}

// bindingJSON renders one repo->agent binding in the JSON shape used by
// GET /repos. Only the trigger field relevant to the binding is included.
func bindingJSON(b config.Binding) map[string]any {
	out := map[string]any{
		"agent":   b.Agent,
		"enabled": b.IsEnabled(),
	}
	switch {
	case b.IsCron():
		out["cron"] = b.Cron
	case b.IsLabel():
		out["labels"] = nilSafe(b.Labels)
	case len(b.Events) > 0:
		out["events"] = nilSafe(b.Events)
	}
	return out
}

// jsonResult encodes v as indented JSON and wraps it in a text CallToolResult.
// The pretty-printing helps humans reading tool output in clients like Claude
// Desktop; machine consumers parse either form equivalently.
func jsonResult(v any) (*mcpgo.CallToolResult, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpgo.NewToolResultErrorFromErr("marshal result", err), nil
	}
	return mcpgo.NewToolResultText(string(body)), nil
}

// nilSafe normalises a nil slice to an empty slice so JSON output is a
// predictable [] rather than null. Consumers — especially LLMs — parse the
// two differently, and we already follow the same convention in REST handlers.
func nilSafe(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// trimmedString reads a required string argument, trims whitespace, and
// returns (value, true) only if the caller supplied a non-empty value.
// Every get_* tool takes an identifier, so the pattern is duplicated enough
// to deserve a helper.
func trimmedString(req mcpgo.CallToolRequest, key string) (string, bool) {
	raw, err := req.RequireString(key)
	if err != nil {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	return raw, true
}

// trimmedStringOptional reads a string argument without treating absence as
// an error. It mirrors trimmedString but returns (value, true) even when the
// caller omits the argument — useful for optional filters like `since`.
func trimmedStringOptional(req mcpgo.CallToolRequest, key string) (string, bool) {
	raw, err := req.RequireString(key)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(raw), true
}

// isTraversalComponent flags identifiers that clean to "." or ".." — the same
// rejection GET /memory/{agent}/{repo} applies. Anything more exotic is
// canonicalised by ai.NormalizeToken downstream and cannot escape the store.
func isTraversalComponent(s string) bool {
	c := filepath.Clean(s)
	return c == "." || c == ".."
}
