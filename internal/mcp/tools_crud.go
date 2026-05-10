package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	daemonfleet "github.com/eloylp/agents/internal/daemon/fleet"
	"github.com/eloylp/agents/internal/fleet"
)

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
		args := req.GetArguments()
		skills, errMsg := stringSliceArg(args["skills"], "skills")
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		canDispatch, errMsg := stringSliceArg(args["can_dispatch"], "can_dispatch")
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if _, ok := args["prompt"]; ok {
			return mcpgo.NewToolResultError("agent prompt bodies are import-only; use prompt_ref"), nil
		}
		a := fleet.Agent{
			WorkspaceID:   req.GetString("workspace", fleet.DefaultWorkspaceID),
			Name:          name,
			Backend:       req.GetString("backend", ""),
			Model:         req.GetString("model", ""),
			PromptID:      req.GetString("prompt_id", ""),
			PromptRef:     req.GetString("prompt_ref", ""),
			ScopeType:     req.GetString("scope_type", ""),
			ScopeRepo:     req.GetString("scope_repo", ""),
			Description:   req.GetString("description", ""),
			Skills:        skills,
			CanDispatch:   canDispatch,
			AllowPRs:      req.GetBool("allow_prs", false),
			AllowDispatch: req.GetBool("allow_dispatch", false),
		}
		// allow_memory: keep AllowMemory nil when the caller omits the field so
		// Agent.IsAllowMemory() returns the documented default of true.
		// Only an explicit true/false in the payload materialises a non-nil
		// pointer, mirroring the binding-enabled convention.
		if v, ok, errMsg := boolPtrArg(args, "allow_memory"); ok {
			a.AllowMemory = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		canonical, err := deps.Fleet.UpsertAgent(a)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create agent", err), nil
		}
		return jsonResult(agentJSON(canonical))
	}
}

// toolUpdateAgent partially updates an agent through the same path as PATCH
// /agents/{name}. Only fields the caller passes are modified; everything else
// is preserved. Returns the canonical merged agent. At least one patch field
// is required (matches the REST handler).
func toolUpdateAgent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		var patch daemonfleet.AgentPatch
		if v, ok := stringPtrArg(args, "backend"); ok {
			patch.Backend = v
		}
		if v, ok := stringPtrArg(args, "model"); ok {
			patch.Model = v
		}
		if _, ok := args["prompt"]; ok {
			return mcpgo.NewToolResultError("agent prompt bodies are import-only; use prompt_ref"), nil
		}
		if v, ok := stringPtrArg(args, "prompt_ref"); ok {
			patch.PromptRef = v
		}
		if v, ok := stringPtrArg(args, "prompt_id"); ok {
			patch.PromptID = v
		}
		if v, ok := stringPtrArg(args, "scope_type"); ok {
			patch.ScopeType = v
		}
		if v, ok := stringPtrArg(args, "scope_repo"); ok {
			patch.ScopeRepo = v
		}
		if v, ok := stringPtrArg(args, "description"); ok {
			patch.Description = v
		}
		if v, ok, errMsg := boolPtrArg(args, "allow_prs"); ok {
			patch.AllowPRs = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := boolPtrArg(args, "allow_dispatch"); ok {
			patch.AllowDispatch = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := boolPtrArg(args, "allow_memory"); ok {
			patch.AllowMemory = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := stringSlicePtrArg(args, "skills"); ok {
			patch.Skills = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := stringSlicePtrArg(args, "can_dispatch"); ok {
			patch.CanDispatch = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		canonical, uerr := deps.Fleet.UpdateAgentPatchInWorkspace(workspace, name, patch)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update agent", uerr), nil
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
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		if err := deps.Fleet.DeleteAgentInWorkspace(workspace, fleet.NormalizeAgentName(name), cascade); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete agent", err), nil
		}
		return jsonResult(map[string]any{
			"status":    "deleted",
			"workspace": fleet.NormalizeWorkspaceID(workspace),
			"name":      fleet.NormalizeAgentName(name),
			"cascade":   cascade,
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
		sk := fleet.Skill{Prompt: req.GetString("prompt", "")}
		canonicalName, canonical, err := deps.Fleet.UpsertSkill(name, sk)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create skill", err), nil
		}
		return jsonResult(map[string]any{
			"name":   canonicalName,
			"prompt": canonical.Prompt,
		})
	}
}

// toolUpdateSkill partially updates a skill through the same path as PATCH
// /skills/{name}. Only fields the caller passes are modified.
func toolUpdateSkill(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		var patch daemonfleet.SkillPatch
		if v, ok := stringPtrArg(args, "prompt"); ok {
			patch.Prompt = v
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		canonicalName, canonical, uerr := deps.Fleet.UpdateSkillPatch(name, patch)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update skill", uerr), nil
		}
		return jsonResult(map[string]any{
			"name":   canonicalName,
			"prompt": canonical.Prompt,
		})
	}
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
		canonical := fleet.NormalizeSkillName(name)
		if err := deps.Fleet.DeleteSkill(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete skill", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

func toolCreateWorkspace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		workspace := fleet.Workspace{
			ID:          req.GetString("id", ""),
			Name:        name,
			Description: req.GetString("description", ""),
		}
		canonical, err := deps.Fleet.UpsertWorkspace(workspace)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create workspace", err), nil
		}
		return jsonResult(workspaceJSON(canonical))
	}
}

func toolUpdateWorkspace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok || workspace == "" {
			workspace = fleet.DefaultWorkspaceID
		}
		args := req.GetArguments()
		var patch daemonfleet.WorkspacePatch
		if v, ok := stringPtrArg(args, "name"); ok {
			patch.Name = v
		}
		if v, ok := stringPtrArg(args, "description"); ok {
			patch.Description = v
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		canonical, err := deps.Fleet.UpdateWorkspacePatch(workspace, patch)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update workspace", err), nil
		}
		return jsonResult(workspaceJSON(canonical))
	}
}

func toolDeleteWorkspace(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok || workspace == "" {
			workspace = fleet.DefaultWorkspaceID
		}
		if err := deps.Fleet.DeleteWorkspace(workspace); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete workspace", err), nil
		}
		return jsonResult(map[string]any{
			"status":    "deleted",
			"workspace": workspace,
		})
	}
}

func toolUpdateWorkspaceGuardrails(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, ok := trimmedStringOptional(req, "workspace")
		if !ok || workspace == "" {
			workspace = fleet.DefaultWorkspaceID
		}
		raw, errMsg := arrayOfAny(req.GetArguments()["guardrails"], "guardrails")
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		refs := make([]fleet.WorkspaceGuardrailRef, 0, len(raw))
		for i, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				return mcpgo.NewToolResultErrorf("guardrails[%d] must be an object", i), nil
			}
			name, ok := m["guardrail_name"].(string)
			if !ok || name == "" {
				return mcpgo.NewToolResultErrorf("guardrails[%d].guardrail_name is required", i), nil
			}
			ref := fleet.WorkspaceGuardrailRef{GuardrailName: name, Position: i}
			if v, ok := m["position"]; ok && v != nil {
				switch n := v.(type) {
				case float64:
					if n != float64(int(n)) {
						return mcpgo.NewToolResultErrorf("guardrails[%d].position must be an integer", i), nil
					}
					ref.Position = int(n)
				case int:
					ref.Position = n
				default:
					return mcpgo.NewToolResultErrorf("guardrails[%d].position must be a number", i), nil
				}
			}
			if v, ok := m["enabled"]; ok && v != nil {
				enabled, ok := v.(bool)
				if !ok {
					return mcpgo.NewToolResultErrorf("guardrails[%d].enabled must be a boolean", i), nil
				}
				ref.Enabled = enabled
			}
			refs = append(refs, ref)
		}
		updated, err := deps.Store.ReplaceWorkspaceGuardrails(workspace, refs)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update workspace guardrails", err), nil
		}
		out := make([]map[string]any, 0, len(updated))
		for _, ref := range updated {
			out = append(out, workspaceGuardrailJSON(ref))
		}
		return jsonResult(out)
	}
}

func toolCreatePrompt(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		prompt := fleet.Prompt{
			ID:          req.GetString("id", ""),
			WorkspaceID: req.GetString("workspace_id", ""),
			Repo:        req.GetString("repo", ""),
			Name:        name,
			Description: req.GetString("description", ""),
			Content:     req.GetString("content", ""),
		}
		canonical, err := deps.Fleet.UpsertPrompt(prompt)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create prompt", err), nil
		}
		return jsonResult(promptJSON(canonical))
	}
}

func toolUpdatePrompt(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		var patch daemonfleet.PromptPatch
		if v, ok := stringPtrArg(args, "description"); ok {
			patch.Description = v
		}
		if v, ok := stringPtrArg(args, "content"); ok {
			patch.Content = v
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		canonical, uerr := deps.Fleet.UpdatePromptPatch(fleet.NormalizePromptName(name), patch)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update prompt", uerr), nil
		}
		return jsonResult(promptJSON(canonical))
	}
}

func toolDeletePrompt(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := fleet.NormalizePromptName(name)
		if err := deps.Fleet.DeletePrompt(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete prompt", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

// toolCreateBackend upserts a backend definition through the same path as
// POST /backends. Returns the canonical (normalized) form so callers see the
// backend the way the store actually persisted it, lowercased name, trimmed
// command, defaults applied for zero-value timeout/max-prompt fields. Empty
// names surface as *store.ErrValidation via Server.UpsertBackend, which
// storeErrStatus maps to a user-actionable error.
func toolCreateBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		models, errMsg := stringSliceArg(req.GetArguments()["models"], "models")
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		b := fleet.Backend{
			Command:        req.GetString("command", ""),
			Models:         models,
			LocalModelURL:  req.GetString("local_model_url", ""),
			TimeoutSeconds: req.GetInt("timeout_seconds", 0),
			MaxPromptChars: req.GetInt("max_prompt_chars", 0),
		}
		canonicalName, canonical, err := deps.Fleet.UpsertBackend(name, b)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create backend", err), nil
		}
		return jsonResult(backendJSON(canonicalName, canonical))
	}
}

// toolUpdateBackend partially updates a backend through the same path as
// PATCH /backends/{name}. Only fields the caller passes are modified. Rejects
// timeout_seconds/max_prompt_chars <= 0 to match REST handler semantics.
func toolUpdateBackend(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		var patch daemonfleet.BackendPatch
		if v, ok := stringPtrArg(args, "command"); ok {
			patch.Command = v
		}
		if v, ok := stringPtrArg(args, "version"); ok {
			patch.Version = v
		}
		if v, ok := stringPtrArg(args, "health_detail"); ok {
			patch.HealthDetail = v
		}
		if v, ok := stringPtrArg(args, "local_model_url"); ok {
			patch.LocalModelURL = v
		}
		if v, ok, errMsg := boolPtrArg(args, "healthy"); ok {
			patch.Healthy = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := stringSlicePtrArg(args, "models"); ok {
			patch.Models = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := intPtrArg(args, "timeout_seconds"); ok {
			if *v <= 0 {
				return mcpgo.NewToolResultError("timeout_seconds must be positive"), nil
			}
			patch.TimeoutSeconds = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := intPtrArg(args, "max_prompt_chars"); ok {
			if *v <= 0 {
				return mcpgo.NewToolResultError("max_prompt_chars must be positive"), nil
			}
			patch.MaxPromptChars = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		canonicalName, canonical, uerr := deps.Fleet.UpdateBackendPatch(name, patch)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update backend", uerr), nil
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
		canonical := fleet.NormalizeBackendName(name)
		if err := deps.Fleet.DeleteBackend(canonical); err != nil {
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
// callers see the repo the way the store actually persisted it, lowercased
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
		r := fleet.Repo{
			WorkspaceID: req.GetString("workspace", fleet.DefaultWorkspaceID),
			Name:        name,
			Enabled:     req.GetBool("enabled", false),
			Use:         bindings,
		}
		canonical, err := deps.Repos.UpsertRepo(r)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create repo", err), nil
		}
		return jsonResult(repoJSON(canonical))
	}
}

// toolUpdateRepo toggles a repo's enabled flag without touching its bindings.
// Same path as PATCH /repos/{owner}/{repo} with {"enabled": ...}. Bindings
// are preserved with their current IDs, unlike create_repo, which is a
// full-replace and would churn binding IDs. Use this when the only change
// is the repo's active state.
func toolUpdateRepo(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		args := req.GetArguments()
		enabledPtr, ok, errMsg := boolPtrArg(args, "enabled")
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if !ok {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		canonical, err := deps.Repos.PatchRepoInWorkspace(workspace, fleet.NormalizeRepoName(name), *enabledPtr)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update repo", err), nil
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
		canonical := fleet.NormalizeRepoName(name)
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		if err := deps.Repos.DeleteRepoInWorkspace(workspace, canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete repo", err), nil
		}
		return jsonResult(map[string]any{
			"status":    "deleted",
			"workspace": fleet.NormalizeWorkspaceID(workspace),
			"name":      canonical,
		})
	}
}

// toolCreateBinding inserts a new binding row for the named repo through the
// same path as POST /repos/{owner}/{repo}/bindings. Returns the persisted
// binding with its generated ID. Trigger validation and agent-reference
// checks happen in the store layer, surfacing as *ErrValidation (user error)
// or *ErrNotFound (user error).
func toolCreateBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		repo, ok := trimmedString(req, "repo")
		if !ok {
			return mcpgo.NewToolResultError("repo is required"), nil
		}
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		b, errMsg := bindingFromReq(req, agent)
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		persisted, err := deps.Repos.CreateBindingInWorkspace(workspace, repo, b)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create binding", err), nil
		}
		return jsonResult(bindingJSON(persisted))
	}
}

// toolUpdateBinding replaces all fields of an existing binding by ID through
// the same path as PATCH /repos/{owner}/{repo}/bindings/{id}. The repo path
// parameter is cross-checked against the stored binding's repo, mismatches
// surface as not-found.
func toolUpdateBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, repo, errMsg := bindingIDAndRepo(req)
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		agent, ok := trimmedString(req, "agent")
		if !ok {
			return mcpgo.NewToolResultError("agent is required"), nil
		}
		b, errMsg := bindingFromReq(req, agent)
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		updated, uerr := deps.Repos.UpdateBindingInWorkspace(workspace, repo, int64(id), b)
		if uerr != nil {
			return mcpgo.NewToolResultErrorFromErr("update binding", uerr), nil
		}
		return jsonResult(bindingJSON(updated))
	}
}

// toolGetBinding fetches one binding by ID, verifying it belongs to the given
// repo. Same path as GET /repos/{owner}/{repo}/bindings/{id}.
func toolGetBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, repo, errMsg := bindingIDAndRepo(req)
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		b, err := deps.Repos.ReadBindingInWorkspace(workspace, repo, int64(id))
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get binding", err), nil
		}
		return jsonResult(bindingJSON(b))
	}
}

// toolDeleteBinding removes a binding by ID through the same path as DELETE
// /repos/{owner}/{repo}/bindings/{id}.
func toolDeleteBinding(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, repo, errMsg := bindingIDAndRepo(req)
		if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		workspace := req.GetString("workspace", fleet.DefaultWorkspaceID)
		if err := deps.Repos.DeleteBindingInWorkspace(workspace, repo, int64(id)); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete binding", err), nil
		}
		return jsonResult(map[string]any{
			"status":    "deleted",
			"workspace": fleet.NormalizeWorkspaceID(workspace),
			"id":        id,
			"repo":      fleet.NormalizeRepoName(repo),
		})
	}
}

// bindingFromReq builds a fleet.Binding from the MCP request fields shared
// by create_binding and update_binding. Returns a non-empty error string when
// a field is present but the wrong type.
func bindingFromReq(req mcpgo.CallToolRequest, agent string) (fleet.Binding, string) {
	args := req.GetArguments()
	labels, errMsg := stringSliceArg(args["labels"], "labels")
	if errMsg != "" {
		return fleet.Binding{}, errMsg
	}
	events, errMsg := stringSliceArg(args["events"], "events")
	if errMsg != "" {
		return fleet.Binding{}, errMsg
	}
	b := fleet.Binding{
		Agent:  agent,
		Labels: labels,
		Events: events,
		Cron:   req.GetString("cron", ""),
	}
	if v, ok, errMsg := boolPtrArg(args, "enabled"); ok {
		b.Enabled = v
	} else if errMsg != "" {
		return fleet.Binding{}, errMsg
	}
	return b, ""
}

// repoJSON renders a Repo in the same wire shape as an element of the
// list_repos / get_repo responses, so create_repo/delete_repo callers consume
// one schema regardless of whether they are reading or writing.
func repoJSON(r fleet.Repo) map[string]any {
	bindings := make([]map[string]any, 0, len(r.Use))
	for _, b := range r.Use {
		bindings = append(bindings, bindingJSON(b))
	}
	return map[string]any{
		"workspace_id": fleet.NormalizeWorkspaceID(r.WorkspaceID),
		"name":         r.Name,
		"enabled":      r.Enabled,
		"bindings":     bindings,
	}
}

// parseBindings decodes the create_repo "bindings" argument into a slice of
// fleet.Binding. The MCP-go request helpers expose string/bool/number
// primitives directly but not nested objects, so we read the raw argument and
// destructure it here. A nil/missing value yields an empty binding list.
//
// Type mismatches are rejected with explicit user errors instead of being
// silently dropped, REST decodes this payload through json.Unmarshal into
// storeBindingJSON, which refuses wrong JSON types. Matching that strictness
// here is what keeps a payload like `{"enabled":"false"}` from being treated
// as omitted (and therefore default-enabled), which would silently flip the
// caller's intended disablement.
//
// Binding.Enabled stays nil when the caller omits the key (the "default
// enabled" case fleet.Binding.IsEnabled relies on). A literal false/true sets
// the pointer so downstream validation sees the user's intent preserved.
//
// The top-level value is also accepted as a JSON-encoded array string, see
// stringSliceArg for the same MCP-transport rationale (some clients
// stringify array params at the JSON-RPC boundary).
func parseBindings(v any) ([]fleet.Binding, string) {
	if v == nil {
		return nil, ""
	}
	raw, errMsg := arrayOfAny(v, "bindings")
	if errMsg != "" {
		return nil, errMsg
	}
	out := make([]fleet.Binding, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("bindings[%d]: must be an object", i)
		}
		var b fleet.Binding
		if v, ok := m["agent"]; ok && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].agent must be a string", i)
			}
			b.Agent = s
		}
		if v, ok := m["cron"]; ok && v != nil {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].cron must be a string", i)
			}
			b.Cron = s
		}
		labels, lErr := stringSliceArg(m["labels"], fmt.Sprintf("bindings[%d].labels", i))
		if lErr != "" {
			return nil, lErr
		}
		b.Labels = labels
		events, eErr := stringSliceArg(m["events"], fmt.Sprintf("bindings[%d].events", i))
		if eErr != "" {
			return nil, eErr
		}
		b.Events = events
		if v, ok := m["enabled"]; ok && v != nil {
			enabled, ok := v.(bool)
			if !ok {
				return nil, fmt.Sprintf("bindings[%d].enabled must be a boolean", i)
			}
			b.Enabled = &enabled
		}
		out = append(out, b)
	}
	return out, ""
}

// stringPtrArg reads an optional string field. Returns (value, true) when the
// key is present, (nil, false) when it is absent or null. Non-string values
// are accepted as their string form via mcpgo.GetString semantics; we only
// want the "present vs missing" distinction here.
func stringPtrArg(args map[string]any, key string) (*string, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	return &s, true
}

// boolPtrArg reads an optional boolean field. Returns (value, true, "") when
// the key is present and well-typed, (nil, false, "") when absent, and
// (nil, false, errMsg) when present but the wrong type so the caller can
// reject the payload instead of silently treating it as absent.
func boolPtrArg(args map[string]any, key string) (*bool, bool, string) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false, ""
	}
	b, ok := v.(bool)
	if !ok {
		return nil, false, fmt.Sprintf("%s must be a boolean", key)
	}
	return &b, true, ""
}

// intPtrArg reads an optional integer field. JSON numbers land as float64 on
// the any interface, so we accept any numeric that is integer-valued.
func intPtrArg(args map[string]any, key string) (*int, bool, string) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false, ""
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return nil, false, fmt.Sprintf("%s must be an integer", key)
		}
		i := int(n)
		return &i, true, ""
	case int:
		return &n, true, ""
	case int64:
		i := int(n)
		return &i, true, ""
	default:
		return nil, false, fmt.Sprintf("%s must be a number", key)
	}
}

// stringSlicePtrArg reads an optional []string field. Returns (&slice, true,
// "") when the key is present and well-typed, including an explicit empty
// array (so callers can clear the list). Missing keys return (nil, false, "").
// Wrong-shape values surface (nil, false, errMsg) so PATCH callers can reject
// rather than silently treat a typo as "preserve".
//
// Accepts the same input shapes as stringSliceArg, including JSON-encoded
// array strings (see that helper for the rationale).
func stringSlicePtrArg(args map[string]any, key string) (*[]string, bool, string) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false, ""
	}
	out, errMsg := stringSliceArg(v, key)
	if errMsg != "" {
		return nil, false, errMsg
	}
	return &out, true, ""
}

// stringSliceArg coerces v into []string for a tool argument.
//
// Returns (nil, "") when v is nil/missing, callers that need to distinguish
// absence from an explicit empty array should use stringSlicePtrArg instead.
// Returns ([]string, "") on success and (nil, errMsg) when v is the wrong
// shape (e.g. a number, a non-string element, or a non-array string that
// doesn't decode as a JSON array).
//
// Accepts:
//   - native []any with string elements (the standard JSON-decoded shape)
//   - native []string (defensive, for callers that pre-decoded)
//   - a JSON-encoded array string (e.g. `["a","b"]`)
//
// The JSON-string path exists because some MCP clients, observed with
// mark3labs/mcp-go when batching tool calls into a single JSON-RPC message ,
// stringify array parameters at the transport boundary. Decoding here keeps
// the server permissive without requiring clients to know about the quirk.
//
// keyForErr is used in error messages, e.g. "skills" produces
// "skills must be an array of strings" or "skills[1] must be a string".
func stringSliceArg(v any, keyForErr string) ([]string, string) {
	if v == nil {
		return nil, ""
	}
	switch raw := v.(type) {
	case []any:
		out := make([]string, 0, len(raw))
		for i, item := range raw {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Sprintf("%s[%d] must be a string", keyForErr, i)
			}
			out = append(out, s)
		}
		return out, ""
	case []string:
		return slices.Clone(raw), ""
	case string:
		var decoded []string
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			return decoded, ""
		}
		return nil, fmt.Sprintf("%s must be an array of strings", keyForErr)
	default:
		return nil, fmt.Sprintf("%s must be an array of strings", keyForErr)
	}
}

// arrayOfAny coerces v into []any for nested-object tool arguments such as
// the create_repo "bindings" payload. Accepts a native []any and a
// JSON-encoded array string for the same MCP-transport reason as
// stringSliceArg. Returns (nil, errMsg) for any other shape.
func arrayOfAny(v any, keyForErr string) ([]any, string) {
	switch raw := v.(type) {
	case []any:
		return raw, ""
	case string:
		var decoded []any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			return decoded, ""
		}
	}
	return nil, fmt.Sprintf("%s must be an array", keyForErr)
}

// bindingIDAndRepo reads the "id" and "repo" parameters shared by
// get_binding, update_binding, and delete_binding. Returns a non-empty
// errMsg on any validation failure.
func bindingIDAndRepo(req mcpgo.CallToolRequest) (int, string, string) {
	id, err := req.RequireInt("id")
	if err != nil {
		return 0, "", err.Error()
	}
	if id <= 0 {
		return 0, "", "id must be a positive integer"
	}
	repo, ok := trimmedString(req, "repo")
	if !ok {
		return 0, "", "repo is required"
	}
	return id, repo, ""
}
