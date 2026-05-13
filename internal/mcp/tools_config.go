package mcp

import (
	"context"

	"github.com/eloylp/agents/internal/fleet"

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

func toolGetRuntime(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		settings, err := deps.Store.ReadRuntimeSettings()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("runtime settings", err), nil
		}
		return jsonResult(settings)
	}
}

func toolUpdateRuntime(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		settings, err := runtimeSettingsFromRequest(req)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		updated, err := deps.Store.WriteRuntimeSettings(settings)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update runtime settings", err), nil
		}
		return jsonResult(updated)
	}
}

func toolUpdateWorkspaceRuntime(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		workspace, err := req.RequireString("workspace")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		image, _ := trimmedStringOptional(req, "runner_image")
		updated, err := deps.Store.SetWorkspaceRunnerImage(workspace, image)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("update workspace runtime", err), nil
		}
		return jsonResult(updated)
	}
}

func runtimeSettingsFromRequest(req mcpgo.CallToolRequest) (fleet.RuntimeSettings, error) {
	current := fleet.RuntimeSettings{}
	if image, ok := trimmedStringOptional(req, "runner_image"); ok {
		current.RunnerImage = image
	}
	if cpus, ok := trimmedStringOptional(req, "cpus"); ok {
		current.Constraints.CPUs = cpus
	}
	if memory, ok := trimmedStringOptional(req, "memory"); ok {
		current.Constraints.Memory = memory
	}
	if network, ok := trimmedStringOptional(req, "network_mode"); ok {
		current.Constraints.NetworkMode = network
	}
	if fs, ok := trimmedStringOptional(req, "filesystem"); ok {
		current.Constraints.Filesystem = fs
	}
	if pids := req.GetInt("pids_limit", 0); pids > 0 {
		current.Constraints.PidsLimit = int64(pids)
	}
	if timeout := req.GetInt("timeout_seconds", 0); timeout > 0 {
		current.Constraints.TimeoutSeconds = timeout
	}
	fleet.NormalizeRuntimeSettings(&current)
	return current, nil
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
