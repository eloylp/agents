package mcp

import (
	"context"
	"maps"
	"slices"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// toolListAgents serialises every agent definition as JSON. Uses the same
// snake_case wire shape as GET /agents so MCP consumers and REST consumers
// see identical data.
func toolListAgents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		agents, err := deps.Store.ReadAgents()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list agents", err), nil
		}
		out := make([]map[string]any, 0, len(agents))
		for _, a := range agents {
			out = append(out, agentJSON(a))
		}
		return jsonResult(out)
	}
}

// toolGetAgent fetches a single agent by name. Matches case-insensitively
// via fleet.NormalizeAgentName, so "Coder" and "coder" both resolve to the
// same entry.
func toolGetAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		agents, err := deps.Store.ReadAgents()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get agent", err), nil
		}
		key := fleet.NormalizeAgentName(name)
		if idx := slices.IndexFunc(agents, func(a fleet.Agent) bool { return a.Name == key }); idx != -1 {
			return jsonResult(agentJSON(agents[idx]))
		}
		return mcpgo.NewToolResultErrorf("agent %q not found", name), nil
	}
}

func toolListSkills(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		skills, err := deps.Store.ReadSkills()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list skills", err), nil
		}
		names := slices.Sorted(maps.Keys(skills))
		out := make([]map[string]any, 0, len(names))
		for _, n := range names {
			s := skills[n]
			out = append(out, map[string]any{
				"name":   n,
				"prompt": s.Prompt,
			})
		}
		return jsonResult(out)
	}
}

// toolGetSkill fetches one skill by its map key. Map lookup is
// case-insensitive via fleet.NormalizeSkillName so agents can reference
// skills without worrying about casing.
func toolGetSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		skills, err := deps.Store.ReadSkills()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get skill", err), nil
		}
		key := fleet.NormalizeSkillName(name)
		s, found := skills[key]
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
		backends, err := deps.Store.ReadBackends()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list backends", err), nil
		}
		names := slices.Sorted(maps.Keys(backends))
		out := make([]map[string]any, 0, len(names))
		for _, n := range names {
			out = append(out, backendJSON(n, backends[n]))
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
		backends, err := deps.Store.ReadBackends()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get backend", err), nil
		}
		key := fleet.NormalizeBackendName(name)
		b, found := backends[key]
		if !found {
			return mcpgo.NewToolResultErrorf("backend %q not found", name), nil
		}
		return jsonResult(backendJSON(key, b))
	}
}

func toolListRepos(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		repos, err := deps.Store.ReadRepos()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list repos", err), nil
		}
		out := make([]map[string]any, 0, len(repos))
		for _, r := range repos {
			out = append(out, repoJSON(r))
		}
		return jsonResult(out)
	}
}

// toolGetRepo fetches one repo by full owner/name, case-insensitive. Lookup
// normalizes the name before searching, matching how the store normalizes.
func toolGetRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		repos, err := deps.Store.ReadRepos()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get repo", err), nil
		}
		key := fleet.NormalizeRepoName(name)
		if idx := slices.IndexFunc(repos, func(r fleet.Repo) bool { return r.Name == key }); idx != -1 {
			return jsonResult(repoJSON(repos[idx]))
		}
		return mcpgo.NewToolResultErrorf("repo %q not found", name), nil
	}
}

// toolGetStatus returns the /status snapshot bytes as text. Keeping the wire
// shape identical to the REST endpoint means MCP and HTTP clients can share
// documentation and tooling.
func toolGetStatus(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := deps.StatusJSON()
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
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		repoName, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}

		repos, err := deps.Store.ReadRepos()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("read repos", err), nil
		}
		want := fleet.NormalizeRepoName(repoName)
		var repo fleet.Repo
		var found bool
		for _, r := range repos {
			if r.Name == want {
				repo = r
				found = true
				break
			}
		}
		if !found || !repo.Enabled {
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
		if _, err := deps.Channels.PushEvent(ctx, ev); err != nil {
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
