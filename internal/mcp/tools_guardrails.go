package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	daemonfleet "github.com/eloylp/agents/internal/daemon/fleet"
	"github.com/eloylp/agents/internal/fleet"
)

// guardrailJSON is the wire shape returned by every guardrail tool. Mirrors
// the REST handler's storeGuardrailJSON so MCP and HTTP consumers see
// identical payloads.
func guardrailJSON(g fleet.Guardrail) map[string]any {
	return map[string]any{
		"name":            g.Name,
		"description":     g.Description,
		"content":         g.Content,
		"default_content": g.DefaultContent,
		"is_builtin":      g.IsBuiltin,
		"enabled":         g.Enabled,
		"position":        g.Position,
	}
}

func toolListGuardrails(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		gs, err := deps.Store.ReadAllGuardrails()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list guardrails", err), nil
		}
		out := make([]map[string]any, 0, len(gs))
		for _, g := range gs {
			out = append(out, guardrailJSON(g))
		}
		return jsonResult(out)
	}
}

func toolGetGuardrail(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		g, err := deps.Store.GetGuardrail(name)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("get guardrail", err), nil
		}
		return jsonResult(guardrailJSON(g))
	}
}

// toolCreateGuardrail upserts an operator-defined guardrail. The is_builtin
// and default_content fields are migration-managed and intentionally not
// part of the wire shape — passing them is silently ignored, matching the
// REST POST /guardrails handler.
func toolCreateGuardrail(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		g := fleet.Guardrail{
			Name:        name,
			Description: req.GetString("description", ""),
			Content:     req.GetString("content", ""),
			Enabled:     req.GetBool("enabled", true),
			Position:    req.GetInt("position", 100),
		}
		canonical, err := deps.Fleet.UpsertGuardrail(g)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("create guardrail", err), nil
		}
		return jsonResult(guardrailJSON(canonical))
	}
}

func toolUpdateGuardrail(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		args := req.GetArguments()
		var patch daemonfleet.GuardrailPatch
		if v, ok := stringPtrArg(args, "description"); ok {
			patch.Description = v
		}
		if v, ok := stringPtrArg(args, "content"); ok {
			patch.Content = v
		}
		if raw, ok := args["enabled"]; ok {
			b, ok := raw.(bool)
			if !ok {
				return mcpgo.NewToolResultError("enabled must be a boolean"), nil
			}
			patch.Enabled = &b
		}
		if raw, ok := args["position"]; ok {
			n, ok := raw.(float64)
			if !ok {
				return mcpgo.NewToolResultError("position must be a number"), nil
			}
			pos := int(n)
			patch.Position = &pos
		}
		if patch.Description == nil && patch.Content == nil && patch.Enabled == nil && patch.Position == nil {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		canonical, err := deps.Fleet.UpdateGuardrailPatch(name, patch)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update guardrail", err), nil
		}
		return jsonResult(guardrailJSON(canonical))
	}
}

func toolDeleteGuardrail(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		canonical := fleet.NormalizeGuardrailName(name)
		if err := deps.Fleet.DeleteGuardrail(canonical); err != nil {
			return mcpgo.NewToolResultErrorFromErr("delete guardrail", err), nil
		}
		return jsonResult(map[string]any{
			"status": "deleted",
			"name":   canonical,
		})
	}
}

func toolResetGuardrail(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name, ok := trimmedString(req, "name")
		if !ok {
			return mcpgo.NewToolResultError("name is required"), nil
		}
		g, err := deps.Fleet.ResetGuardrail(name)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("reset guardrail", err), nil
		}
		return jsonResult(guardrailJSON(g))
	}
}
