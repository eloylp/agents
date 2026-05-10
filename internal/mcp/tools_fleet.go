package mcp

import (
	"context"
	"maps"
	"slices"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/workflow"
)

// toolListAgents serialises every agent definition as JSON. Uses the same
// snake_case wire shape as GET /agents so MCP consumers and REST consumers
// see identical data.
func toolListAgents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace := strings.TrimSpace(req.GetString("workspace", ""))
		agents, err := deps.Store.ReadAgents()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list agents", err), nil
		}
		out := make([]map[string]any, 0, len(agents))
		for _, a := range agents {
			if workspace != "" && fleet.NormalizeWorkspaceID(a.WorkspaceID) != fleet.NormalizeWorkspaceID(workspace) {
				continue
			}
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
		workspace := fleet.NormalizeWorkspaceID(req.GetString("workspace", fleet.DefaultWorkspaceID))
		if idx := slices.IndexFunc(agents, func(a fleet.Agent) bool {
			return a.Name == key && fleet.NormalizeWorkspaceID(a.WorkspaceID) == workspace
		}); idx != -1 {
			return jsonResult(agentJSON(agents[idx]))
		}
		return mcpgo.NewToolResultErrorf("agent %q not found in workspace %q", name, workspace), nil
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

func toolListPrompts(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		prompts, err := deps.Store.ReadPrompts()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list prompts", err), nil
		}
		out := make([]map[string]any, 0, len(prompts))
		for _, p := range prompts {
			out = append(out, promptJSON(p))
		}
		return jsonResult(out)
	}
}

func toolListWorkspaces(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspaces, err := deps.Store.ReadWorkspaces()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list workspaces", err), nil
		}
		out := make([]map[string]any, 0, len(workspaces))
		for _, w := range workspaces {
			out = append(out, workspaceJSON(w))
		}
		return jsonResult(out)
	}
}

func toolGetWorkspace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			workspace = fleet.DefaultWorkspaceID
		}
		w, err := deps.Store.ReadWorkspace(workspace)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get workspace", err), nil
		}
		return jsonResult(workspaceJSON(w))
	}
}

func toolListWorkspaceGuardrails(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok || workspace == "" {
			workspace = fleet.DefaultWorkspaceID
		}
		refs, err := deps.Store.ReadWorkspaceGuardrails(workspace)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list workspace guardrails", err), nil
		}
		out := make([]map[string]any, 0, len(refs))
		for _, ref := range refs {
			out = append(out, workspaceGuardrailJSON(ref))
		}
		return jsonResult(out)
	}
}

func toolGetPrompt(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		prompts, err := deps.Store.ReadPrompts()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get prompt", err), nil
		}
		key := fleet.NormalizePromptName(name)
		if idx := slices.IndexFunc(prompts, func(p fleet.Prompt) bool { return p.Name == key }); idx != -1 {
			return jsonResult(promptJSON(prompts[idx]))
		}
		return mcpgo.NewToolResultErrorf("prompt %q not found", name), nil
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
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace := strings.TrimSpace(req.GetString("workspace", ""))
		repos, err := deps.Store.ReadRepos()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list repos", err), nil
		}
		out := make([]map[string]any, 0, len(repos))
		for _, r := range repos {
			if workspace != "" && fleet.NormalizeWorkspaceID(r.WorkspaceID) != fleet.NormalizeWorkspaceID(workspace) {
				continue
			}
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
		workspace := fleet.NormalizeWorkspaceID(req.GetString("workspace", fleet.DefaultWorkspaceID))
		if idx := slices.IndexFunc(repos, func(r fleet.Repo) bool {
			return r.Name == key && fleet.NormalizeWorkspaceID(r.WorkspaceID) == workspace
		}); idx != -1 {
			return jsonResult(repoJSON(repos[idx]))
		}
		return mcpgo.NewToolResultErrorf("repo %q not found in workspace %q", name, workspace), nil
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
		workspaceID := strings.TrimSpace(req.GetString("workspace", fleet.DefaultWorkspaceID))
		if workspaceID == "" {
			workspaceID = fleet.DefaultWorkspaceID
		}

		repos, err := deps.Store.ReadRepos()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("read repos", err), nil
		}
		want := fleet.NormalizeRepoName(repoName)
		idx := slices.IndexFunc(repos, func(r fleet.Repo) bool {
			repoWorkspace := r.WorkspaceID
			if repoWorkspace == "" {
				repoWorkspace = fleet.DefaultWorkspaceID
			}
			return r.Name == want && repoWorkspace == workspaceID
		})
		if idx < 0 || !repos[idx].Enabled {
			return mcpgo.NewToolResultErrorf("repo %q not found or disabled", repoName), nil
		}
		repo := repos[idx]

		ev := workflow.Event{
			ID:          workflow.GenEventID(),
			WorkspaceID: workspaceID,
			Repo:        workflow.RepoRef{FullName: repo.Name, Enabled: repo.Enabled},
			Kind:        "agents.run",
			Actor:       "mcp",
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
