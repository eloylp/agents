package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// toolGetConfig returns the redacted effective config JSON. The bytes are
// pass-through from ConfigReader.ConfigJSON so REST and MCP callers see the
// exact same payload, including secret redaction and omitted fields like
// proxy.extra_body.
func toolGetConfig(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		body, err := deps.Config.ConfigJSON()
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
		body, err := deps.Config.ExportYAML()
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
		counts, err := deps.Config.ImportYAML([]byte(body), mode)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("import config", err), nil
		}
		return jsonResult(counts)
	}
}
