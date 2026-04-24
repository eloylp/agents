package mcp

import (
	"context"
	"encoding/json"
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
