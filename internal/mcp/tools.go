package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/observe"
	"github.com/eloylp/agents/internal/workflow"
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

// toolListAgents serialises every agent definition as JSON. Uses the same
// snake_case wire shape as GET /agents so MCP consumers and REST consumers
// see identical data.
func toolListAgents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		cfg := deps.Config.Config()
		out := make([]map[string]any, 0, len(cfg.Agents))
		for _, a := range cfg.Agents {
			out = append(out, agentJSON(a))
		}
		return jsonResult(out)
	}
}

// toolGetAgent fetches a single agent by name. Matches case-insensitively like
// AgentByName, so "Coder" and "coder" both resolve to the same entry.
func toolGetAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cfg := deps.Config.Config()
		a, found := cfg.AgentByName(name)
		if !found {
			return mcpgo.NewToolResultErrorf("agent %q not found", name), nil
		}
		return jsonResult(agentJSON(a))
	}
}

func toolListSkills(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		cfg := deps.Config.Config()
		out := make([]map[string]any, 0, len(cfg.Skills))
		names := make([]string, 0, len(cfg.Skills))
		for n := range cfg.Skills {
			names = append(names, n)
		}
		sortStrings(names)
		for _, n := range names {
			s := cfg.Skills[n]
			out = append(out, map[string]any{
				"name":   n,
				"prompt": s.Prompt,
			})
		}
		return jsonResult(out)
	}
}

// toolGetSkill fetches one skill by its map key. Map lookup is
// case-insensitive via config.NormalizeSkillName so agents can reference
// skills without worrying about casing.
func toolGetSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cfg := deps.Config.Config()
		key := config.NormalizeSkillName(name)
		s, found := cfg.Skills[key]
		if !found {
			return mcpgo.NewToolResultErrorf("skill %q not found", name), nil
		}
		return jsonResult(map[string]any{
			"name":   key,
			"prompt": s.Prompt,
		})
	}
}

func toolListBackends(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		cfg := deps.Config.Config()
		names := make([]string, 0, len(cfg.Daemon.AIBackends))
		for n := range cfg.Daemon.AIBackends {
			names = append(names, n)
		}
		sortStrings(names)
		out := make([]map[string]any, 0, len(names))
		for _, n := range names {
			out = append(out, backendJSON(n, cfg.Daemon.AIBackends[n]))
		}
		return jsonResult(out)
	}
}

// toolGetBackend fetches one backend by its map key. Returns the same
// snake_case fields as list_backends entries.
func toolGetBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cfg := deps.Config.Config()
		key := config.NormalizeBackendName(name)
		b, found := cfg.Daemon.AIBackends[key]
		if !found {
			return mcpgo.NewToolResultErrorf("backend %q not found", name), nil
		}
		return jsonResult(backendJSON(key, b))
	}
}

func toolListRepos(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		cfg := deps.Config.Config()
		out := make([]map[string]any, 0, len(cfg.Repos))
		for _, r := range cfg.Repos {
			bindings := make([]map[string]any, 0, len(r.Use))
			for _, b := range r.Use {
				bindings = append(bindings, bindingJSON(b))
			}
			out = append(out, map[string]any{
				"name":     r.Name,
				"enabled":  r.Enabled,
				"bindings": bindings,
			})
		}
		return jsonResult(out)
	}
}

// toolGetRepo fetches one repo by full owner/name, case-insensitive. Lookup
// delegates to Config.RepoByName for parity with the REST path.
func toolGetRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cfg := deps.Config.Config()
		r, found := cfg.RepoByName(name)
		if !found {
			return mcpgo.NewToolResultErrorf("repo %q not found", name), nil
		}
		bindings := make([]map[string]any, 0, len(r.Use))
		for _, b := range r.Use {
			bindings = append(bindings, bindingJSON(b))
		}
		return jsonResult(map[string]any{
			"name":     r.Name,
			"enabled":  r.Enabled,
			"bindings": bindings,
		})
	}
}

// toolGetStatus returns the /status snapshot bytes as text. Keeping the wire
// shape identical to the REST endpoint means MCP and HTTP clients can share
// documentation and tooling.
func toolGetStatus(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := deps.Status.StatusJSON()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("status snapshot", err), nil
		}
		return mcpgo.NewToolResultText(string(body)), nil
	}
}

// toolTriggerAgent mirrors POST /run: validate the repo is known and enabled,
// enqueue an agents.run event, and return the event ID so the caller can
// correlate with trace data later.
func toolTriggerAgent(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		agent, err := req.RequireString("agent")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		repoName, err := req.RequireString("repo")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		agent = strings.TrimSpace(agent)
		repoName = strings.TrimSpace(repoName)
		if agent == "" || repoName == "" {
			return mcpgo.NewToolResultError("agent and repo are required"), nil
		}

		cfg := deps.Config.Config()
		repo, ok := cfg.RepoByName(repoName)
		if !ok || !repo.Enabled {
			return mcpgo.NewToolResultErrorf("repo %q not found or disabled", repoName), nil
		}

		ev := workflow.Event{
			ID:    workflow.GenEventID(),
			Repo:  workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
			Kind:  "agents.run",
			Actor: "mcp",
			Payload: map[string]any{
				"target_agent": agent,
			},
		}
		if err := deps.Queue.PushEvent(ctx, ev); err != nil {
			deps.Logger.Error().Err(err).Str("agent", agent).Str("repo", repoName).Msg("mcp: failed to enqueue on-demand agent run")
			return mcpgo.NewToolResultErrorf("event queue full: %v", err), nil
		}
		deps.Logger.Info().Str("agent", agent).Str("repo", repoName).Str("event_id", ev.ID).Msg("mcp: on-demand agent run queued")
		return jsonResult(map[string]string{
			"status":   "queued",
			"agent":    agent,
			"repo":     repoName,
			"event_id": ev.ID,
		})
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

// bindingJSON renders one repo→agent binding in the JSON shape used by
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

func sortStrings(xs []string) { sort.Strings(xs) }

// toolListEvents enumerates recent events. The optional `since` argument
// parses as RFC3339; a blank or unparseable value behaves like no filter,
// matching GET /events so the two surfaces stay aligned.
func toolListEvents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		var since time.Time
		if raw, _ := trimmedStringOptional(req, "since"); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				since = t
			}
		}
		events := deps.Observe.ListEvents(since)
		if events == nil {
			events = []observe.TimestampedEvent{}
		}
		return jsonResult(events)
	}
}

// toolListTraces returns the 200 most recent spans verbatim. The Span JSON
// shape already matches GET /traces so clients can parse both surfaces with
// the same decoder.
func toolListTraces(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		spans := deps.Observe.ListTraces()
		if spans == nil {
			spans = []observe.Span{}
		}
		return jsonResult(spans)
	}
}

// toolGetTrace returns every span for a given root event ID. An empty result
// is reported as a tool-level error so clients surface a clear "not found"
// message rather than an empty array that callers might mistake for success.
func toolGetTrace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "root_event_id")
		if !ok {
			return mcpgo.NewToolResultError("root_event_id is required"), nil
		}
		spans := deps.Observe.TracesByRootEventID(id)
		if len(spans) == 0 {
			return mcpgo.NewToolResultErrorf("trace %q not found", id), nil
		}
		return jsonResult(spans)
	}
}

// toolGetTraceSteps returns the tool-loop transcript for one span. A span
// with no recorded steps (non-claude backend, or span still in flight)
// yields an empty array rather than an error — the span itself is valid,
// it just has no transcript.
func toolGetTraceSteps(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := trimmedString(req, "span_id")
		if !ok {
			return mcpgo.NewToolResultError("span_id is required"), nil
		}
		steps := deps.Observe.ListSteps(id)
		if steps == nil {
			steps = []workflow.TraceStep{}
		}
		return jsonResult(steps)
	}
}

// mcpGraphNode mirrors the node payload used by GET /graph so consumers
// parsing both surfaces share a decoder. Status is intentionally omitted
// here: runtime-state wiring belongs in a follow-up; callers can resolve
// per-agent health via list_agents.
type mcpGraphNode struct {
	ID string `json:"id"`
}

// mcpGraphEdge mirrors the edge payload used by GET /graph, with timestamps
// normalised to RFC3339 for wire parity.
type mcpGraphEdge struct {
	From       string                `json:"from"`
	To         string                `json:"to"`
	Count      int                   `json:"count"`
	Dispatches []mcpGraphDispatch    `json:"dispatches"`
}

type mcpGraphDispatch struct {
	At     string `json:"at"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Reason string `json:"reason"`
}

// toolGetGraph returns the dispatch interaction graph. Nodes are seeded from
// the configured fleet so agents with no dispatch history still show up; any
// edge endpoints not in the current config (e.g. agents removed after they
// dispatched) are added so the graph stays self-consistent.
func toolGetGraph(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		edges := deps.Observe.ListEdges()
		cfg := deps.Config.Config()

		seen := make(map[string]struct{})
		for _, a := range cfg.Agents {
			seen[a.Name] = struct{}{}
		}
		for _, e := range edges {
			seen[e.From] = struct{}{}
			seen[e.To] = struct{}{}
		}
		names := make([]string, 0, len(seen))
		for name := range seen {
			names = append(names, name)
		}
		sortStrings(names)
		nodes := make([]mcpGraphNode, 0, len(names))
		for _, name := range names {
			nodes = append(nodes, mcpGraphNode{ID: name})
		}

		wireEdges := make([]mcpGraphEdge, 0, len(edges))
		for _, e := range edges {
			recs := make([]mcpGraphDispatch, 0, len(e.Dispatches))
			for _, d := range e.Dispatches {
				recs = append(recs, mcpGraphDispatch{
					At:     d.At.UTC().Format(time.RFC3339),
					Repo:   d.Repo,
					Number: d.Number,
					Reason: d.Reason,
				})
			}
			wireEdges = append(wireEdges, mcpGraphEdge{
				From:       e.From,
				To:         e.To,
				Count:      e.Count,
				Dispatches: recs,
			})
		}

		return jsonResult(map[string]any{
			"nodes": nodes,
			"edges": wireEdges,
		})
	}
}

// toolGetDispatches returns the current dispatch counters.
func toolGetDispatches(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return jsonResult(deps.DispatchStats.DispatchStats())
	}
}

// toolGetMemory returns the markdown memory for an (agent, repo) pair.
// A defensive filepath.Clean-based traversal check rejects blatant attempts
// (".", "..") before the call reaches the memory reader, which already
// canonicalises identifiers via ai.NormalizeToken.
func toolGetMemory(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		if isTraversalComponent(agent) || isTraversalComponent(repo) {
			return mcpgo.NewToolResultError("invalid agent or repo path"), nil
		}
		content, mtime, found, err := deps.Memory.ReadMemory(agent, repo)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("read memory", err), nil
		}
		if !found {
			return mcpgo.NewToolResultErrorf("memory for %s/%s not found", agent, repo), nil
		}
		out := map[string]any{
			"agent":   agent,
			"repo":    repo,
			"content": content,
		}
		if !mtime.IsZero() {
			out["mtime"] = mtime.UTC().Format(time.RFC3339)
		}
		return jsonResult(out)
	}
}

// isTraversalComponent flags identifiers that clean to "." or ".." — the same
// rejection GET /memory/{agent}/{repo} applies. Anything more exotic is
// canonicalised by ai.NormalizeToken downstream and cannot escape the store.
func isTraversalComponent(s string) bool {
	c := filepath.Clean(s)
	return c == "." || c == ".."
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

// toolGetConfig returns the redacted effective config JSON. The bytes are
// pass-through from ConfigReader.ConfigJSON so REST and MCP callers see the
// exact same payload — including secret redaction and omitted fields like
// proxy.extra_body.
func toolGetConfig(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := deps.ConfigBytes.ConfigJSON()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("config snapshot", err), nil
		}
		return mcpgo.NewToolResultText(string(body)), nil
	}
}

// toolExportConfig returns the CRUD-mutable sections of the fleet config as a
// YAML fragment matching GET /export. The body is round-trippable through
// POST /import so operators can export, edit, and re-import from a single
// MCP session.
func toolExportConfig(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := deps.ConfigBytes.ExportYAML()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("export config", err), nil
		}
		return mcpgo.NewToolResultText(string(body)), nil
	}
}

// toolImportConfig writes a YAML payload into the store using the same code
// path as POST /import. Validation, store, and cron-reload errors are
// surfaced to the caller as tool errors; on success the per-section counts
// are returned as JSON.
func toolImportConfig(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := req.RequireString("yaml")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		mode, _ := trimmedStringOptional(req, "mode")
		counts, err := deps.ConfigImport.ImportYAML([]byte(body), mode)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("import config", err), nil
		}
		return jsonResult(counts)
	}
}

// toolCreateAgent upserts an agent definition through the same path as POST
// /agents. Returns the canonical (normalized) form so callers see the agent
// the way the store actually persisted it. Empty names, unknown backends, and
// model/skill validation failures surface as tool errors via the store's
// *ErrValidation / *ErrConflict types.
func toolCreateAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		a := config.AgentDef{
			Name:          name,
			Backend:       req.GetString("backend", ""),
			Model:         req.GetString("model", ""),
			Prompt:        req.GetString("prompt", ""),
			Description:   req.GetString("description", ""),
			Skills:        req.GetStringSlice("skills", nil),
			CanDispatch:   req.GetStringSlice("can_dispatch", nil),
			AllowPRs:      req.GetBool("allow_prs", false),
			AllowDispatch: req.GetBool("allow_dispatch", false),
		}
		canonical, err := deps.AgentWrite.UpsertAgent(a)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create agent", err), nil
		}
		return jsonResult(agentJSON(canonical))
	}
}

// toolDeleteAgent removes an agent through the same path as DELETE
// /agents/{name}. cascade=true also drops repo bindings that reference the
// agent; without it, a referenced agent surfaces a *store.ErrConflict so
// callers can prompt for cascade explicitly rather than silently mutating
// repo bindings.
func toolDeleteAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		cascade := req.GetBool("cascade", false)
		if err := deps.AgentWrite.DeleteAgent(config.NormalizeAgentName(name), cascade); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete agent", err), nil
		}
		return jsonResult(map[string]any{
			"status":  "deleted",
			"name":    config.NormalizeAgentName(name),
			"cascade": cascade,
		})
	}
}

// toolCreateSkill upserts a skill through the same path as POST /skills.
// Returns the canonical (normalized) form so callers see the skill the way the
// store actually persisted it. Empty names surface as *store.ErrValidation via
// Server.UpsertSkill, which storeErrStatus maps to a user-actionable error.
func toolCreateSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		sk := config.SkillDef{Prompt: req.GetString("prompt", "")}
		canonicalName, canonical, err := deps.SkillWrite.UpsertSkill(name, sk)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create skill", err), nil
		}
		return jsonResult(map[string]any{
			"name":   canonicalName,
			"prompt": canonical.Prompt,
		})
	}
}

// toolCreateBackend upserts a backend definition through the same path as
// POST /backends. Returns the canonical (normalized) form so callers see the
// backend the way the store actually persisted it — lowercased name, trimmed
// command, defaults applied for zero-value timeout/max-prompt fields. Empty
// names surface as *store.ErrValidation via Server.UpsertBackend, which
// storeErrStatus maps to a user-actionable error.
func toolCreateBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		b := config.AIBackendConfig{
			Command:          req.GetString("command", ""),
			Models:           req.GetStringSlice("models", nil),
			LocalModelURL:    req.GetString("local_model_url", ""),
			TimeoutSeconds:   req.GetInt("timeout_seconds", 0),
			MaxPromptChars:   req.GetInt("max_prompt_chars", 0),
			RedactionSaltEnv: req.GetString("redaction_salt_env", ""),
		}
		canonicalName, canonical, err := deps.BackendWrite.UpsertBackend(name, b)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create backend", err), nil
		}
		return jsonResult(backendJSON(canonicalName, canonical))
	}
}

// toolDeleteBackend removes a backend through the same path as DELETE
// /backends/{name}. If any agent still references the backend the store
// surfaces a *store.ErrConflict, which the caller sees as a user-actionable
// error.
func toolDeleteBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeBackendName(name)
		if err := deps.BackendWrite.DeleteBackend(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete backend", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// toolCreateRepo upserts a repo definition (and its bindings) through the
// same path as POST /repos. Returns the canonical (normalized) form so
// callers see the repo the way the store actually persisted it — lowercased
// owner/name, lowercased binding agents, trimmed cron, lowercased events.
// Empty names surface as *store.ErrValidation via Server.UpsertRepo, which
// storeErrStatus maps to a user-actionable error. Binding validation errors
// from the store (unknown agent, bad cron, trigger ambiguity) propagate as
// tool errors.
func toolCreateRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		bindings, bErr := parseBindings(req.GetArguments()["bindings"])
		if bErr != "" {
			return mcpgo.NewToolResultError(bErr), nil
		}
		r := config.RepoDef{
			Name:    name,
			Enabled: req.GetBool("enabled", false),
			Use:     bindings,
		}
		canonical, err := deps.RepoWrite.UpsertRepo(r)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create repo", err), nil
		}
		return jsonResult(repoJSON(canonical))
	}
}

// toolDeleteRepo removes a repo (and cascades its bindings) through the same
// path as DELETE /repos/{owner}/{repo}. The underlying store delete is
// idempotent for unknown names; a *store.ErrConflict surfaces if deleting
// would leave the fleet with zero enabled repos, which the caller sees as a
// user-actionable error.
func toolDeleteRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeRepoName(name)
		if err := deps.RepoWrite.DeleteRepo(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete repo", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// repoJSON renders a RepoDef in the same wire shape as an element of the
// list_repos / get_repo responses, so create_repo/delete_repo callers consume
// one schema regardless of whether they are reading or writing.
func repoJSON(r config.RepoDef) map[string]any {
	bindings := make([]map[string]any, 0, len(r.Use))
	for _, b := range r.Use {
		bindings = append(bindings, bindingJSON(b))
	}
	return map[string]any{
		"name":     r.Name,
		"enabled":  r.Enabled,
		"bindings": bindings,
	}
}

// parseBindings decodes the create_repo "bindings" argument into a slice of
// config.Binding. The MCP-go request helpers expose string/bool/number
// primitives directly but not nested objects, so we read the raw argument and
// destructure it here. A nil/missing value yields an empty binding list. Any
// non-array or non-object element is reported to the caller as a validation
// error rather than silently dropped, matching REST's JSON-decode behaviour.
//
// Binding.Enabled stays nil when the caller omits the key (the "default
// enabled" case config.Binding.IsEnabled relies on). A literal false/true sets
// the pointer so downstream validation sees the user's intent preserved.
func parseBindings(v any) ([]config.Binding, string) {
	if v == nil {
		return nil, ""
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, "bindings must be an array"
	}
	out := make([]config.Binding, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("bindings[%d]: must be an object", i)
		}
		agent, _ := m["agent"].(string)
		cron, _ := m["cron"].(string)
		b := config.Binding{
			Agent:  agent,
			Labels: stringSliceFromAny(m["labels"]),
			Events: stringSliceFromAny(m["events"]),
			Cron:   cron,
		}
		if v, ok := m["enabled"]; ok {
			if enabled, ok := v.(bool); ok {
				b.Enabled = &enabled
			}
		}
		out = append(out, b)
	}
	return out, ""
}

// stringSliceFromAny best-effort decodes a JSON array of strings. Non-string
// elements are skipped so the store validator surfaces the bad binding rather
// than the tool layer guessing at the user's intent.
func stringSliceFromAny(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toolDeleteSkill removes a skill through the same path as DELETE
// /skills/{name}. If any agent still references the skill the store surfaces a
// *store.ErrConflict, which the caller sees as a user-actionable error.
func toolDeleteSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := config.NormalizeSkillName(name)
		if err := deps.SkillWrite.DeleteSkill(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete skill", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}
