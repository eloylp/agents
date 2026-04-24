package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/config"
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
