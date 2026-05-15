package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eloylp/agents/internal/store"

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
		patch, err := runtimeSettingsPatchFromRequest(req)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		updated, err := deps.Store.PatchRuntimeSettings(patch)
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

func runtimeSettingsPatchFromRequest(req mcpgo.CallToolRequest) (store.RuntimeSettingsPatch, error) {
	var patch store.RuntimeSettingsPatch
	if image, ok := trimmedStringOptional(req, "runner_image"); ok {
		patch.RunnerImage = &image
	}
	if cpus, ok := trimmedStringOptional(req, "cpus"); ok {
		patch.Constraints.CPUs = &cpus
	}
	if memory, ok := trimmedStringOptional(req, "memory"); ok {
		patch.Constraints.Memory = &memory
	}
	if network, ok := trimmedStringOptional(req, "network_mode"); ok {
		patch.Constraints.NetworkMode = &network
	}
	if pids, ok, err := optionalIntArg(req, "pids_limit"); err != nil {
		return store.RuntimeSettingsPatch{}, err
	} else if ok {
		pids64 := int64(pids)
		patch.Constraints.PidsLimit = &pids64
	}
	if timeout, ok, err := optionalIntArg(req, "timeout_seconds"); err != nil {
		return store.RuntimeSettingsPatch{}, err
	} else if ok {
		patch.Constraints.TimeoutSeconds = &timeout
	}
	return patch, nil
}

func optionalIntArg(req mcpgo.CallToolRequest, key string) (int, bool, error) {
	raw, ok := req.GetArguments()[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case int:
		return v, true, nil
	case int64:
		if v < int64(math.MinInt) || v > int64(math.MaxInt) {
			return 0, false, fmt.Errorf("%s is outside int range", key)
		}
		return int(v), true, nil
	case float64:
		if math.Trunc(v) != v || v < float64(math.MinInt) || v > float64(math.MaxInt) {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return int(v), true, nil
	case json.Number:
		i, err := strconv.ParseInt(v.String(), 10, 0)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return int(i), true, nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return i, true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", key)
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
