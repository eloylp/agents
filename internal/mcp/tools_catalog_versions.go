package mcp

import (
	"context"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/fleet"
)

func registerCatalogVersionTools(srv *server.MCPServer, deps Deps) {
	registerPromptCatalogVersionTools(srv, deps)
	registerSkillCatalogVersionTools(srv, deps)
	registerGuardrailCatalogVersionTools(srv, deps)
}

func registerPromptCatalogVersionTools(srv *server.MCPServer, deps Deps) {
	promptSelector := []mcpgo.ToolOption{
		mcpgo.WithString("id", mcpgo.Description("Stable prompt id. Preferred for scripts and required when name/scope is ambiguous.")),
		mcpgo.WithString("name", mcpgo.Description("Prompt display name. If id is omitted, resolves with optional scope, or workspace_id/repo.")),
		mcpgo.WithString("scope", mcpgo.Description("Optional catalog scope path: global, workspace, or workspace/owner/repo.")),
		mcpgo.WithString("workspace_id", mcpgo.Description("Optional workspace id used with name resolution.")),
		mcpgo.WithString("repo", mcpgo.Description("Optional repo name used with name resolution. Requires workspace_id.")),
	}
	srv.AddTool(mcpgo.NewTool("list_prompt_versions",
		append([]mcpgo.ToolOption{mcpgo.WithDescription("List prompt catalog versions, newest first. Same data as GET /prompts/{id}/versions.")}, promptSelector...)...,
	), toolListPromptVersions(deps))
	srv.AddTool(mcpgo.NewTool("list_prompt_version_references",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("List live agent references that resolve to a prompt version."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Prompt version id.")),
		}, promptSelector...)...,
	), toolListPromptVersionReferences(deps))
	srv.AddTool(mcpgo.NewTool("publish_prompt_version",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("Publish a draft or proposal prompt version after verifying it belongs to the selected prompt."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Draft or proposal prompt version id.")),
		}, promptSelector...)...,
	), toolPublishPromptVersion(deps))
}

func registerSkillCatalogVersionTools(srv *server.MCPServer, deps Deps) {
	skillSelector := []mcpgo.ToolOption{
		mcpgo.WithString("id", mcpgo.Description("Stable skill id. Preferred, and required for scoped skills that may share display names.")),
		mcpgo.WithString("name", mcpgo.Description("Legacy global skill display name fallback.")),
	}
	srv.AddTool(mcpgo.NewTool("list_skill_versions",
		append([]mcpgo.ToolOption{mcpgo.WithDescription("List skill catalog versions, newest first. Same data as GET /skills/{id}/versions.")}, skillSelector...)...,
	), toolListSkillVersions(deps))
	srv.AddTool(mcpgo.NewTool("list_skill_version_references",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("List live agent references that resolve to a skill version."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Skill version id.")),
		}, skillSelector...)...,
	), toolListSkillVersionReferences(deps))
	srv.AddTool(mcpgo.NewTool("publish_skill_version",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("Publish a draft or proposal skill version after verifying it belongs to the selected skill."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Draft or proposal skill version id.")),
		}, skillSelector...)...,
	), toolPublishSkillVersion(deps))
}

func registerGuardrailCatalogVersionTools(srv *server.MCPServer, deps Deps) {
	guardrailSelector := []mcpgo.ToolOption{
		mcpgo.WithString("id", mcpgo.Description("Stable guardrail id. Preferred for scoped guardrails.")),
		mcpgo.WithString("name", mcpgo.Description("Legacy global guardrail display name fallback.")),
	}
	srv.AddTool(mcpgo.NewTool("list_guardrail_versions",
		append([]mcpgo.ToolOption{mcpgo.WithDescription("List guardrail catalog versions, newest first. Same data as GET /guardrails/{id}/versions.")}, guardrailSelector...)...,
	), toolListGuardrailVersions(deps))
	srv.AddTool(mcpgo.NewTool("list_guardrail_version_references",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("List live workspace references that resolve to a guardrail version."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Guardrail version id.")),
		}, guardrailSelector...)...,
	), toolListGuardrailVersionReferences(deps))
	srv.AddTool(mcpgo.NewTool("publish_guardrail_version",
		append([]mcpgo.ToolOption{
			mcpgo.WithDescription("Publish a draft or proposal guardrail version after verifying it belongs to the selected guardrail."),
			mcpgo.WithString("version_id", mcpgo.Required(), mcpgo.Description("Draft or proposal guardrail version id.")),
		}, guardrailSelector...)...,
	), toolPublishGuardrailVersion(deps))
}

func toolListPromptVersions(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, err := resolvePromptRef(deps, req)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("resolve prompt", err), nil
		}
		versions, err := deps.Store.ListPromptVersions(ref)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list prompt versions", err), nil
		}
		return jsonResult(versions)
	}
}

func toolListPromptVersionReferences(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, err := resolvePromptRef(deps, req)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("resolve prompt", err), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		refs, err := deps.Store.ListPromptVersionReferences(ref, versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list prompt version references", err), nil
		}
		return jsonResult(refs)
	}
}

func toolPublishPromptVersion(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, err := resolvePromptRef(deps, req)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("resolve prompt", err), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		if err := ensureCatalogVersion(deps.Store.ListPromptVersions, ref, versionID); err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish prompt version", err), nil
		}
		prompt, err := deps.Fleet.PublishPromptVersion(versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish prompt version", err), nil
		}
		return jsonResult(promptJSON(prompt))
	}
}

func toolListSkillVersions(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := skillRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versions, err := deps.Store.ListSkillVersions(fleet.NormalizeSkillName(ref))
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list skill versions", err), nil
		}
		return jsonResult(versions)
	}
}

func toolListSkillVersionReferences(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := skillRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		refs, err := deps.Store.ListSkillVersionReferences(fleet.NormalizeSkillName(ref), versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list skill version references", err), nil
		}
		return jsonResult(refs)
	}
}

func toolPublishSkillVersion(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := skillRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		canonical := fleet.NormalizeSkillName(ref)
		if err := ensureCatalogVersion(deps.Store.ListSkillVersions, canonical, versionID); err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish skill version", err), nil
		}
		name, skill, err := deps.Fleet.PublishSkillVersion(versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish skill version", err), nil
		}
		return jsonResult(skillJSON(name, skill))
	}
}

func toolListGuardrailVersions(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := guardrailRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versions, err := deps.Store.ListGuardrailVersions(fleet.NormalizeGuardrailName(ref))
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list guardrail versions", err), nil
		}
		return jsonResult(versions)
	}
}

func toolListGuardrailVersionReferences(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := guardrailRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		refs, err := deps.Store.ListGuardrailVersionReferences(fleet.NormalizeGuardrailName(ref), versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list guardrail version references", err), nil
		}
		return jsonResult(refs)
	}
}

func toolPublishGuardrailVersion(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		ref, ok := guardrailRefArg(req)
		if !ok {
			return mcpgo.NewToolResultError("id or name is required"), nil
		}
		versionID, err := req.RequireString("version_id")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		canonical := fleet.NormalizeGuardrailName(ref)
		if err := ensureCatalogVersion(deps.Store.ListGuardrailVersions, canonical, versionID); err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish guardrail version", err), nil
		}
		guardrail, err := deps.Fleet.PublishGuardrailVersion(versionID)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("publish guardrail version", err), nil
		}
		return jsonResult(guardrailJSON(guardrail))
	}
}

func ensureCatalogVersion(list func(string) ([]fleet.CatalogVersion, error), ref, versionID string) error {
	versions, err := list(ref)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if version.ID == versionID {
			return nil
		}
	}
	return fmt.Errorf("version %q not found for %q", versionID, ref)
}
