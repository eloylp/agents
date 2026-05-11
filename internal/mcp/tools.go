package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/fleet"
)

// registerTools wires the core tool set onto the MCP server. Handlers read
// config from deps.Config and enqueue events via deps.Channels, matching the
// semantics of the equivalent REST endpoints.
func registerTools(srv *server.MCPServer, deps Deps) {
	srv.AddTool(
		mcpgo.NewTool("list_agents",
			mcpgo.WithDescription("List configured agents with backend, model, skills, and dispatch wiring. Pass workspace to filter to one workspace; omit to list all agents."),
			mcpgo.WithString("workspace",
				mcpgo.Description("Optional workspace id/name filter. Omit to list all workspace-local agents."),
			),
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
			mcpgo.WithString("workspace",
				mcpgo.Description("Workspace id/name. Defaults to Default for compatibility."),
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
		mcpgo.NewTool("list_prompts",
			mcpgo.WithDescription("List every prompt catalog entry. Prompts are reusable task/personality contracts that may be global, workspace-scoped, or repo-scoped."),
		),
		toolListPrompts(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_workspaces",
			mcpgo.WithDescription("List every workspace. Workspaces scope repos, agents, runs, memory, graph layout, and budgets."),
		),
		toolListWorkspaces(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_skill",
			mcpgo.WithDescription("Fetch one skill's full prompt body by stable id. Legacy global display-name lookup is also accepted."),
			mcpgo.WithString("id",
				mcpgo.Description("Stable skill id. Preferred, and required for scoped skills that may share display names."),
			),
			mcpgo.WithString("name",
				mcpgo.Description("Legacy global skill display name fallback."),
			),
		),
		toolGetSkill(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_prompt",
			mcpgo.WithDescription("Fetch one prompt catalog entry by stable id, or by name plus optional workspace_id/repo when unambiguous."),
			mcpgo.WithString("id",
				mcpgo.Description("Stable prompt id. Preferred for scripts and required when name/scope is ambiguous."),
			),
			mcpgo.WithString("name",
				mcpgo.Description("Prompt display name. If id is omitted, resolves with optional workspace_id/repo."),
			),
			mcpgo.WithString("workspace_id",
				mcpgo.Description("Optional workspace id used with name resolution."),
			),
			mcpgo.WithString("repo",
				mcpgo.Description("Optional repo name used with name resolution. Requires workspace_id."),
			),
		),
		toolGetPrompt(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_workspace",
			mcpgo.WithDescription("Fetch one workspace by id or display name."),
			mcpgo.WithString("workspace",
				mcpgo.Required(),
				mcpgo.Description("Workspace id or display name."),
			),
		),
		toolGetWorkspace(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_guardrails",
			mcpgo.WithDescription("List every prompt guardrail (operator-defined policy blocks prepended to every agent's composed prompt). Includes the shipped 'security' default and any operator-added rules. Returns enabled and disabled rows in render order (position ASC, name ASC)."),
		),
		toolListGuardrails(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_guardrail",
			mcpgo.WithDescription("Fetch one guardrail by name. Returns content, default_content (built-ins only), is_builtin, enabled, position."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Guardrail name (case-insensitive). Built-in: 'security'."),
			),
		),
		toolGetGuardrail(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("list_workspace_guardrails",
			mcpgo.WithDescription("List the global guardrail references selected by one workspace. Defaults to Default when workspace is omitted."),
			mcpgo.WithString("workspace",
				mcpgo.Description("Workspace id or display name. Defaults to default."),
			),
		),
		toolListWorkspaceGuardrails(deps),
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
			mcpgo.WithDescription("List configured repos and their agent bindings (labels, events, cron). Pass workspace to filter to one workspace; omit to list all repos."),
			mcpgo.WithString("workspace",
				mcpgo.Description("Optional workspace id/name filter. Omit to list all workspace-local repos."),
			),
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
			mcpgo.WithString("workspace",
				mcpgo.Description("Workspace id/name. Defaults to default."),
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
			mcpgo.WithString("workspace",
				mcpgo.Description("Workspace id or display name. Defaults to default."),
			),
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
				mcpgo.WithDescription("Fetch the recorded transcript for one span. Each row carries a `kind`: `tool` for paired tool_use+tool_result rounds (with input/output summaries and duration), or `thinking` for assistant text blocks emitted between tool calls (full text in input_summary). Steps are ordered by occurrence."),
				mcpgo.WithString("span_id",
					mcpgo.Required(),
					mcpgo.Description("Span ID to fetch steps for."),
				),
			),
			toolGetTraceSteps(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("get_trace_prompt",
				mcpgo.WithDescription("Return the composed prompt the daemon sent to the AI CLI for this span, the exact System+User text the agent saw. Stored gzipped on the trace row; this tool decompresses on the fly. Returns an error when no prompt was recorded (pre-009-migration spans). Same path as GET /traces/{span_id}/prompt."),
				mcpgo.WithString("span_id",
					mcpgo.Required(),
					mcpgo.Description("Span ID to fetch the prompt for."),
				),
			),
			toolGetTracePrompt(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("get_graph",
				mcpgo.WithDescription("Return the agent interaction graph: every configured agent as a node plus observed inter-agent dispatch edges with counts and individual dispatch records."),
			),
			toolGetGraph(deps),
		)
	}
	if deps.Engine != nil {
		srv.AddTool(
			mcpgo.NewTool("get_dispatches",
				mcpgo.WithDescription("Return current dispatch counters: requested, enqueued, deduped, and drop reasons (no whitelist, no opt-in, self, depth, fanout)."),
			),
			toolGetDispatches(deps),
		)
	}
	if deps.Store != nil {
		srv.AddTool(
			mcpgo.NewTool("get_memory",
				mcpgo.WithDescription("Return the stored markdown memory for an (agent, repo) pair. Memory is loaded and persisted around every run when the agent has allow_memory: true (the default), uniformly across cron, webhooks, dispatch, POST /run, and trigger_agent."),
				mcpgo.WithString("agent",
					mcpgo.Required(),
					mcpgo.Description("Agent name."),
				),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo in owner/repo or owner_repo form."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id. Defaults to default when omitted."),
				),
			),
			toolGetMemory(deps),
		)
	}
	if deps.Config != nil {
		srv.AddTool(
			mcpgo.NewTool("get_config",
				mcpgo.WithDescription("Return the current fleet config snapshot as JSON. Same wire shape as GET /config."),
			),
			toolGetConfig(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("export_config",
				mcpgo.WithDescription("Return the CRUD-mutable fleet config (backends, agents, skills, repos, guardrails, token_budgets) as a YAML fragment. Same body as GET /export, suitable for piping back into POST /import."),
			),
			toolExportConfig(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("import_config",
				mcpgo.WithDescription("Write a YAML config fragment (backends, agents, skills, repos, guardrails, token_budgets) into the store. mode=\"\" or \"merge\" upserts; mode=\"replace\" prunes entries not in the payload. Returns per-section counts. Same path as POST /import."),
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
	if deps.Fleet != nil {
		srv.AddTool(
			mcpgo.NewTool("create_skill",
				mcpgo.WithDescription("Create or update a skill. Upsert semantics: a write to an existing id overwrites it. Returns the canonical skill persisted by the store. Same path as POST /skills."),
				mcpgo.WithString("id",
					mcpgo.Description("Optional stable skill id. Omit to use the normalized name for global skills or derive a scoped id from workspace/repo/name."),
				),
				mcpgo.WithString("workspace_id",
					mcpgo.Description("Optional workspace id for workspace- or repo-scoped skill visibility. Omit for global."),
				),
				mcpgo.WithString("repo",
					mcpgo.Description("Optional repo name for repo-scoped skill visibility. Requires workspace_id."),
				),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("User-facing skill display name. Lowercased and trimmed by the store."),
				),
				mcpgo.WithString("prompt",
					mcpgo.Description("Skill prompt body (reusable guidance injected into composing agents' system prompt)."),
				),
			),
			toolCreateSkill(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_skill",
				mcpgo.WithDescription("Partially update a skill by stable id. Legacy global display-name lookup is also accepted. Only fields present in the call are modified. Same path as PATCH /skills/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable skill id. Preferred, and required for scoped skills that may share display names."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Legacy global skill display name fallback."),
				),
				mcpgo.WithString("prompt",
					mcpgo.Description("New skill prompt body. Omit to leave unchanged."),
				),
			),
			toolUpdateSkill(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_skill",
				mcpgo.WithDescription("Delete a skill by stable id. Legacy global display-name lookup is also accepted. Fails if any agent still references the skill. Same path as DELETE /skills/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable skill id. Preferred, and required for scoped skills that may share display names."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Legacy global skill display name fallback."),
				),
			),
			toolDeleteSkill(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("create_workspace",
				mcpgo.WithDescription("Create or update a workspace and seed its built-in guardrail references. Same path as POST /workspaces."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Workspace display name."),
				),
				mcpgo.WithString("id",
					mcpgo.Description("Optional stable workspace id. Omit to derive a URL-safe id from name."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Short human-readable workspace description."),
				),
			),
			toolCreateWorkspace(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_workspace",
				mcpgo.WithDescription("Partially update a workspace by id or display name. Same path as PATCH /workspaces/{workspace}."),
				mcpgo.WithString("workspace",
					mcpgo.Required(),
					mcpgo.Description("Workspace id or display name."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("New workspace display name. Omit to leave unchanged."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("New workspace description. Omit to leave unchanged."),
				),
			),
			toolUpdateWorkspace(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_workspace",
				mcpgo.WithDescription("Delete a workspace by id or display name. Fails for Default and while agents or repos still reference it. Same path as DELETE /workspaces/{workspace}."),
				mcpgo.WithString("workspace",
					mcpgo.Required(),
					mcpgo.Description("Workspace id or display name."),
				),
			),
			toolDeleteWorkspace(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_workspace_guardrails",
				mcpgo.WithDescription("Replace a workspace's selected guardrail references. Same path as PUT /workspaces/{workspace}/guardrails."),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id or display name. Defaults to default."),
				),
				mcpgo.WithArray("guardrails",
					mcpgo.Required(),
					mcpgo.Description("Replacement list of guardrail references. Each item needs guardrail_name; enabled defaults false if omitted; position defaults to list order when zero."),
					mcpgo.Items(map[string]any{
						"type": "object",
						"properties": map[string]any{
							"guardrail_name": map[string]any{"type": "string", "description": "Stable guardrail id. Legacy global display names are also accepted when unambiguous."},
							"position":       map[string]any{"type": "integer", "description": "Render order. Lower renders first."},
							"enabled":        map[string]any{"type": "boolean", "description": "Whether this workspace reference is active."},
						},
						"required": []any{"guardrail_name"},
					}),
				),
			),
			toolUpdateWorkspaceGuardrails(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("create_prompt",
				mcpgo.WithDescription("Create or update a prompt catalog entry. Empty workspace_id/repo makes it global; workspace_id with optional repo scopes visibility."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Prompt name."),
				),
				mcpgo.WithString("id",
					mcpgo.Description("Optional stable prompt id. Omit to derive a URL-safe id from name and scope."),
				),
				mcpgo.WithString("workspace_id",
					mcpgo.Description("Optional workspace id for workspace- or repo-scoped prompt visibility. Omit for global."),
				),
				mcpgo.WithString("repo",
					mcpgo.Description("Optional repo name for repo-scoped prompt visibility. Requires workspace_id."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Short human-readable prompt description."),
				),
				mcpgo.WithString("content",
					mcpgo.Description("Prompt body agents receive when they reference this prompt."),
				),
			),
			toolCreatePrompt(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_prompt",
				mcpgo.WithDescription("Partially update a prompt by stable id, or by name plus optional workspace_id/repo when unambiguous. Same path as PATCH /prompts/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable prompt id. Preferred for scripts and required when name/scope is ambiguous."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Prompt display name. If id is omitted, resolves with optional workspace_id/repo."),
				),
				mcpgo.WithString("workspace_id",
					mcpgo.Description("Optional workspace id used with name resolution."),
				),
				mcpgo.WithString("repo",
					mcpgo.Description("Optional repo name used with name resolution. Requires workspace_id."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("New prompt description. Omit to leave unchanged."),
				),
				mcpgo.WithString("content",
					mcpgo.Description("New prompt content. Omit to leave unchanged."),
				),
			),
			toolUpdatePrompt(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_prompt",
				mcpgo.WithDescription("Delete a prompt by stable id, or by name plus optional workspace_id/repo when unambiguous. Fails while any agent references it. Same path as DELETE /prompts/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable prompt id. Preferred for scripts and required when name/scope is ambiguous."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Prompt display name. If id is omitted, resolves with optional workspace_id/repo."),
				),
				mcpgo.WithString("workspace_id",
					mcpgo.Description("Optional workspace id used with name resolution."),
				),
				mcpgo.WithString("repo",
					mcpgo.Description("Optional repo name used with name resolution. Requires workspace_id."),
				),
			),
			toolDeletePrompt(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("create_guardrail",
				mcpgo.WithDescription("Create or update an operator-defined prompt guardrail. Guardrails may be global, workspace-scoped, or repo-scoped. Upsert semantics preserve built-in flags (is_builtin / default_content). Same path as POST /guardrails."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable guardrail id. Optional; derived from scope and name when omitted."),
				),
				mcpgo.WithString("workspace_id",
					mcpgo.Description("Optional workspace visibility scope. Required when repo is set."),
				),
				mcpgo.WithString("repo",
					mcpgo.Description("Optional repo visibility scope inside workspace_id."),
				),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Guardrail name. Lowercased, trimmed, and dash-joined by the store."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Short label shown in the dashboard list."),
				),
				mcpgo.WithString("content",
					mcpgo.Description("The policy text prepended to every agent's composed prompt at render time."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("Whether the renderer includes this guardrail. Defaults to true."),
				),
				mcpgo.WithNumber("position",
					mcpgo.Description("Render order: lower first, ties broken by name. Built-in 'security' uses 0; operator-added rows default to 100."),
				),
			),
			toolCreateGuardrail(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_guardrail",
				mcpgo.WithDescription("Partially update a guardrail by stable id. Legacy global display-name lookup is also accepted. is_builtin and default_content are migration-managed and cannot be patched. Same path as PATCH /guardrails/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable guardrail id. Preferred, and required for scoped guardrails that may share display names."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Legacy global guardrail display name fallback."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("New description. Omit to leave unchanged."),
				),
				mcpgo.WithString("content",
					mcpgo.Description("New policy text. Omit to leave unchanged."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("New enabled state. Omit to leave unchanged."),
				),
				mcpgo.WithNumber("position",
					mcpgo.Description("New render position. Omit to leave unchanged."),
				),
			),
			toolUpdateGuardrail(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_guardrail",
				mcpgo.WithDescription("Delete a guardrail by stable id. Legacy global display-name lookup is also accepted. Built-ins can be deleted too; the dashboard double-confirms in the UI. Same path as DELETE /guardrails/{id}."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable guardrail id. Preferred, and required for scoped guardrails that may share display names."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Legacy global guardrail display name fallback."),
				),
			),
			toolDeleteGuardrail(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("reset_guardrail",
				mcpgo.WithDescription("Reset a built-in guardrail's content back to its migration-seeded default_content by stable id. Legacy global display-name lookup is also accepted. Same path as POST /guardrails/{id}/reset."),
				mcpgo.WithString("id",
					mcpgo.Description("Stable guardrail id. Preferred, and required for scoped guardrails that may share display names."),
				),
				mcpgo.WithString("name",
					mcpgo.Description("Legacy global guardrail display name fallback. Must identify a built-in."),
				),
			),
			toolResetGuardrail(deps),
		)
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
			),
			toolCreateBackend(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_backend",
				mcpgo.WithDescription("Partially update a backend by name. Only fields present in the call are modified; everything else is preserved. Same path as PATCH /backends/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Backend name (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithString("command",
					mcpgo.Description("Path to the CLI binary. Omit to leave unchanged."),
				),
				mcpgo.WithString("version",
					mcpgo.Description("Discovered CLI version string. Omit to leave unchanged."),
				),
				mcpgo.WithArray("models",
					mcpgo.Description("Model catalog for this backend. Omit to leave unchanged; pass an empty array to clear."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithBoolean("healthy",
					mcpgo.Description("Discovered backend health flag. Omit to leave unchanged."),
				),
				mcpgo.WithString("health_detail",
					mcpgo.Description("Human-readable health diagnostic text. Omit to leave unchanged."),
				),
				mcpgo.WithString("local_model_url",
					mcpgo.Description("Optional OpenAI-compatible base URL for local backends. Omit to leave unchanged."),
				),
				mcpgo.WithNumber("timeout_seconds",
					mcpgo.Description("Per-run CLI timeout in seconds. Must be > 0 when supplied. Omit to leave unchanged."),
				),
				mcpgo.WithNumber("max_prompt_chars",
					mcpgo.Description("Maximum composed prompt length. Must be > 0 when supplied. Omit to leave unchanged."),
				),
			),
			toolUpdateBackend(deps),
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
		srv.AddTool(
			mcpgo.NewTool("create_agent",
				mcpgo.WithDescription("Create or update an agent. Upsert semantics: a write to an existing name overwrites it. Returns the canonical (normalized) agent persisted by the store. Same path as POST /agents."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Agent name. Lowercased and trimmed by the store."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to Default for compatibility."),
				),
				mcpgo.WithString("backend",
					mcpgo.Description("AI backend name (must exist in the fleet). Required for runnable agents."),
				),
				mcpgo.WithString("model",
					mcpgo.Description("Optional model identifier; must be present in the backend's model catalog."),
				),
				mcpgo.WithString("prompt_ref",
					mcpgo.Description("Visible prompt name to reference from this workspace-local agent. Use prompt_id when names are ambiguous."),
				),
				mcpgo.WithString("prompt_id",
					mcpgo.Description("Stable prompt id to reference. Preferred when multiple visible prompts share a name."),
				),
				mcpgo.WithString("scope_type",
					mcpgo.Description("Agent scope: workspace or repo. Defaults to workspace."),
				),
				mcpgo.WithString("scope_repo",
					mcpgo.Description("Repo name required when scope_type is repo."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Required short human-readable description used for identification and inter-agent routing context."),
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
				mcpgo.WithBoolean("allow_memory",
					mcpgo.Description("Load and persist this agent's memory uniformly across every run kind (cron, webhooks, dispatch, POST /run, trigger_agent). Defaults to true; set to false for stateless agents."),
				),
			),
			toolCreateAgent(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_agent",
				mcpgo.WithDescription("Partially update an agent by name. Only fields present in the call are modified; everything else is preserved. Use an empty array to clear a slice field. Same path as PATCH /agents/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Agent name (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name used to resolve the agent. Defaults to Default for compatibility."),
				),
				mcpgo.WithString("backend",
					mcpgo.Description("AI backend name. Omit to leave unchanged."),
				),
				mcpgo.WithString("model",
					mcpgo.Description("Model identifier. Omit to leave unchanged."),
				),
				mcpgo.WithString("prompt_ref",
					mcpgo.Description("Visible prompt name. Omit to leave unchanged. Use prompt_id when names are ambiguous."),
				),
				mcpgo.WithString("prompt_id",
					mcpgo.Description("Stable prompt id. Omit to leave unchanged."),
				),
				mcpgo.WithString("scope_type",
					mcpgo.Description("Agent scope: workspace or repo. Omit to leave unchanged."),
				),
				mcpgo.WithString("scope_repo",
					mcpgo.Description("Repo name for repo-scoped agents. Omit to leave unchanged."),
				),
				mcpgo.WithString("description",
					mcpgo.Description("Required short description. Omit to leave unchanged."),
				),
				mcpgo.WithArray("skills",
					mcpgo.Description("List of skill names. Omit to leave unchanged; pass an empty array to clear."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithArray("can_dispatch",
					mcpgo.Description("Whitelist of dispatchable targets. Omit to leave unchanged; pass an empty array to clear."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithBoolean("allow_prs",
					mcpgo.Description("Allow this agent to open/edit PRs. Omit to leave unchanged."),
				),
				mcpgo.WithBoolean("allow_dispatch",
					mcpgo.Description("Allow other agents to dispatch this agent. Omit to leave unchanged."),
				),
				mcpgo.WithBoolean("allow_memory",
					mcpgo.Description("Load and persist this agent's memory uniformly across every run kind. Defaults to true; set to false for stateless agents. Omit to leave unchanged."),
				),
			),
			toolUpdateAgent(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_agent",
				mcpgo.WithDescription("Delete an agent by name. Without cascade=true the call fails with a conflict error if any repo binding still references the agent. Same path as DELETE /agents/{name}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Agent name (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name used to resolve the agent. Defaults to Default for compatibility."),
				),
				mcpgo.WithBoolean("cascade",
					mcpgo.Description("When true, also remove repo bindings that reference the agent. Defaults to false."),
				),
			),
			toolDeleteAgent(deps),
		)
	}
	if deps.Repos != nil {
		srv.AddTool(
			mcpgo.NewTool("create_repo",
				mcpgo.WithDescription("Create or update a repo and its bindings. Upsert semantics: a write to an existing name overwrites it, replacing the bindings list. Returns the canonical (normalized) repo persisted by the store. Same path as POST /repos."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\". Lowercased and trimmed by the store."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("Whether the repo is active. Defaults to false, callers must opt in explicitly, matching POST /repos."),
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
			mcpgo.NewTool("update_repo",
				mcpgo.WithDescription("Toggle a repo's enabled flag without touching its bindings. Bindings are preserved with their current IDs, unlike create_repo, which is a full-replace and would churn binding IDs. Use this when the only change is the repo's active state. Same path as PATCH /repos/{owner}/{repo}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Required(),
					mcpgo.Description("New value for the repo's active flag."),
				),
			),
			toolUpdateRepo(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_repo",
				mcpgo.WithDescription("Delete a repo (and its bindings) by full name. Same path as DELETE /repos/{owner}/{repo}."),
				mcpgo.WithString("name",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" (case-insensitive; matched after lowercasing)."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
			),
			toolDeleteRepo(deps),
		)
	}
	registerRunnersTools(srv, deps)
	if deps.Repos != nil {
		srv.AddTool(
			mcpgo.NewTool("get_binding",
				mcpgo.WithDescription("Fetch one binding by ID, verifying it belongs to the given repo. Same path as GET /repos/{owner}/{repo}/bindings/{id}."),
				mcpgo.WithNumber("id",
					mcpgo.Required(),
					mcpgo.Description("Binding ID (from list_repos or get_repo)."),
				),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" the binding belongs to."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
			),
			toolGetBinding(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("create_binding",
				mcpgo.WithDescription("Create a new binding on a repo. The binding wires one agent to exactly one trigger (labels, events, or cron). Returns the persisted binding including its generated ID. Same path as POST /repos/{owner}/{repo}/bindings."),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" the binding attaches to."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
				mcpgo.WithString("agent",
					mcpgo.Required(),
					mcpgo.Description("Agent name to bind (must exist in the fleet)."),
				),
				mcpgo.WithArray("labels",
					mcpgo.Description("Label-triggered binding: fire when one of these labels is applied. Mutually exclusive with events/cron."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithArray("events",
					mcpgo.Description("Event-triggered binding: fire on these GitHub event kinds (e.g. issues.opened). Mutually exclusive with labels/cron."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithString("cron",
					mcpgo.Description("Cron-triggered binding: 5-field schedule expression for autonomous runs. Mutually exclusive with labels/events."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("Whether this binding is active. Absent = enabled; only explicit false disables."),
				),
			),
			toolCreateBinding(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("update_binding",
				mcpgo.WithDescription("Replace all fields of an existing binding by ID. The agent, labels, events, cron, and enabled flag are all overwritten. Same path as PATCH /repos/{owner}/{repo}/bindings/{id}."),
				mcpgo.WithNumber("id",
					mcpgo.Required(),
					mcpgo.Description("Binding ID (from list_repos or get_repo)."),
				),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" the binding belongs to."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
				mcpgo.WithString("agent",
					mcpgo.Required(),
					mcpgo.Description("Agent name to bind (must exist in the fleet)."),
				),
				mcpgo.WithArray("labels",
					mcpgo.Description("Label-triggered binding. Mutually exclusive with events/cron."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithArray("events",
					mcpgo.Description("Event-triggered binding. Mutually exclusive with labels/cron."),
					mcpgo.Items(map[string]any{"type": "string"}),
				),
				mcpgo.WithString("cron",
					mcpgo.Description("Cron expression. Mutually exclusive with labels/events."),
				),
				mcpgo.WithBoolean("enabled",
					mcpgo.Description("Whether this binding is active. Absent = enabled; only explicit false disables."),
				),
			),
			toolUpdateBinding(deps),
		)
		srv.AddTool(
			mcpgo.NewTool("delete_binding",
				mcpgo.WithDescription("Delete a binding by ID. Same path as DELETE /repos/{owner}/{repo}/bindings/{id}."),
				mcpgo.WithNumber("id",
					mcpgo.Required(),
					mcpgo.Description("Binding ID."),
				),
				mcpgo.WithString("repo",
					mcpgo.Required(),
					mcpgo.Description("Repo full name \"owner/repo\" the binding belongs to."),
				),
				mcpgo.WithString("workspace",
					mcpgo.Description("Workspace id/name. Defaults to default."),
				),
			),
			toolDeleteBinding(deps),
		)
	}
	if deps.Store != nil {
		registerBudgetTools(srv, deps)
	}
}

// agentJSON converts a fleet.Agent to the snake_case map shape used by
// the REST API, so the MCP and HTTP surfaces stay aligned.
func agentJSON(a fleet.Agent) map[string]any {
	return map[string]any{
		"id":             a.ID,
		"workspace_id":   fleet.NormalizeWorkspaceID(a.WorkspaceID),
		"name":           a.Name,
		"backend":        a.Backend,
		"model":          a.Model,
		"skills":         nilSafe(a.Skills),
		"prompt":         a.Prompt,
		"prompt_id":      a.PromptID,
		"prompt_ref":     a.PromptRef,
		"scope_type":     a.ScopeType,
		"scope_repo":     a.ScopeRepo,
		"description":    a.Description,
		"allow_prs":      a.AllowPRs,
		"allow_dispatch": a.AllowDispatch,
		"allow_memory":   a.IsAllowMemory(),
		"can_dispatch":   nilSafe(a.CanDispatch),
	}
}

func promptJSON(p fleet.Prompt) map[string]any {
	return map[string]any{
		"id":           p.ID,
		"workspace_id": p.WorkspaceID,
		"repo":         p.Repo,
		"name":         p.Name,
		"description":  p.Description,
		"content":      p.Content,
	}
}

func skillJSON(id string, s fleet.Skill) map[string]any {
	return map[string]any{
		"id":           id,
		"workspace_id": s.WorkspaceID,
		"repo":         s.Repo,
		"name":         s.Name,
		"prompt":       s.Prompt,
	}
}

func workspaceJSON(w fleet.Workspace) map[string]any {
	return map[string]any{
		"id":          w.ID,
		"name":        w.Name,
		"description": w.Description,
	}
}

func workspaceGuardrailJSON(ref fleet.WorkspaceGuardrailRef) map[string]any {
	return map[string]any{
		"workspace_id":   ref.WorkspaceID,
		"guardrail_name": ref.GuardrailName,
		"position":       ref.Position,
		"enabled":        ref.Enabled,
	}
}

// backendJSON renders one AI backend entry in the snake_case shape shared
// between list_backends and get_backend.
func backendJSON(name string, b fleet.Backend) map[string]any {
	return map[string]any{
		"name":             name,
		"command":          b.Command,
		"version":          b.Version,
		"models":           nilSafe(b.Models),
		"healthy":          b.Healthy,
		"health_detail":    b.HealthDetail,
		"local_model_url":  b.LocalModelURL,
		"timeout_seconds":  b.TimeoutSeconds,
		"max_prompt_chars": b.MaxPromptChars,
	}
}

// bindingJSON renders one repo->agent binding in the JSON shape used by
// GET /repos. All trigger fields are included so the shape stays stable
// for consumers; unused triggers appear as empty values. The id field is
// included only when > 0 (unset for bindings that haven't yet been persisted
// to the store, matching the omitempty behaviour on the REST side).
func bindingJSON(b fleet.Binding) map[string]any {
	out := map[string]any{
		"agent":   b.Agent,
		"labels":  nilSafe(b.Labels),
		"events":  nilSafe(b.Events),
		"cron":    b.Cron,
		"enabled": b.IsEnabled(),
	}
	if b.ID > 0 {
		out["id"] = b.ID
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
// predictable [] rather than null. Consumers, especially LLMs, parse the
// two differently, and we already follow the same convention in REST handlers.
func nilSafe[T any](xs []T) []T {
	if xs == nil {
		return []T{}
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
// caller omits the argument, useful for optional filters like `since`.
func trimmedStringOptional(req mcpgo.CallToolRequest, key string) (string, bool) {
	raw, err := req.RequireString(key)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(raw), true
}

// isTraversalComponent flags identifiers that clean to "." or "..", the same
// rejection GET /memory/{agent}/{repo} applies. Anything more exotic is
// canonicalised by ai.NormalizeToken downstream and cannot escape the store.
func isTraversalComponent(s string) bool {
	c := filepath.Clean(s)
	return c == "." || c == ".."
}
