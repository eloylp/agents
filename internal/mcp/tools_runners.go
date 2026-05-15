package mcp

import (
	"context"
	"errors"
	"net/http/httptest"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	daemonrunners "github.com/eloylp/agents/internal/daemon/runners"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// registerRunnersTools wires the /runners surface as MCP tools.
// Mirrors the REST endpoints exposed by internal/daemon/runners:
// list / delete / retry.
func registerRunnersTools(srv *server.MCPServer, deps Deps) {
	if deps.RunnersH == nil {
		return
	}
	srv.AddTool(
		mcpgo.NewTool("list_runners",
			mcpgo.WithDescription("List runner rows: each row is one event in the durable queue, expanded into per-agent rows once traces have been recorded for it. While an event is in-flight (no traces yet) one row appears with agent=null and status=enqueued|running. Once completed, one row per fanned-out agent appears with status=success|error and may include sanitized error_kind/error_detail failure metadata. Same path as GET /runners."),
			mcpgo.WithString("status",
				mcpgo.Description("Optional filter on the underlying event_queue row state: \"enqueued\", \"running\", or \"completed\". Empty returns every state."),
			),
			mcpgo.WithString("workspace",
				mcpgo.Description("Optional workspace id/name. Defaults to Default."),
			),
			mcpgo.WithNumber("limit",
				mcpgo.Description("Maximum events to return. Output rows can exceed this when events fan out to multiple agents. Defaults to 100."),
			),
			mcpgo.WithNumber("offset",
				mcpgo.Description("Pagination offset on event rows."),
			),
		),
		toolListRunners(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("delete_runner",
			mcpgo.WithDescription("Remove a runner (event_queue row) by id. Best-effort: a worker that has already received the QueuedEvent from the in-memory channel may still run it; the row simply won't appear in /runners afterwards. Same path as DELETE /runners/{id}."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Row id from list_runners."),
			),
		),
		toolDeleteRunner(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("retry_runner",
			mcpgo.WithDescription("Re-enqueue a runner by copying the original event blob into a fresh event_queue row and pushing onto the channel. The source row stays as audit history. Re-runs every fanned-out agent (event-level retry). Rejected with a conflict when the source row is still in the running state. Same path as POST /runners/{id}/retry."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Row id from list_runners to retry."),
			),
		),
		toolRetryRunner(deps),
	)
}

func toolListRunners(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		status, _ := trimmedStringOptional(req, "status")
		workspace, _ := trimmedStringOptional(req, "workspace")
		limit := mcpInt(req, "limit", 0)
		offset := mcpInt(req, "offset", 0)
		resp, err := deps.RunnersH.List(workspace, status, limit, offset)
		if err != nil {
			return mcpgo.NewToolResultErrorf("list runners: %v", err), nil
		}
		return jsonResult(resp)
	}
}

func toolDeleteRunner(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := mcpRequiredInt64(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		if err := deps.RunnersH.Delete(id); err != nil {
			if errors.Is(err, store.ErrRunnerNotFound) {
				return mcpgo.NewToolResultErrorf("runner %d not found", id), nil
			}
			return mcpgo.NewToolResultErrorf("delete runner %d: %v", id, err), nil
		}
		return jsonResult(map[string]any{"deleted": id})
	}
}

func toolRetryRunner(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := mcpRequiredInt64(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		// Retry needs an *http.Request so it can derive a request context;
		// httptest.NewRequest constructs one carrying the MCP tool-call
		// context. No HTTP machinery is invoked beyond that.
		r := httptest.NewRequest("POST", "/runners/0/retry", nil).WithContext(ctx)
		newID, err := deps.RunnersH.Retry(r, id)
		switch {
		case errors.Is(err, store.ErrRunnerNotFound):
			return mcpgo.NewToolResultErrorf("runner %d not found", id), nil
		case errors.Is(err, daemonrunners.ErrRunnerRunning):
			return mcpgo.NewToolResultErrorf("runner %d is running and cannot be retried", id), nil
		case errors.Is(err, workflow.ErrEventQueueFull):
			return mcpgo.NewToolResultErrorf("event queue full, retry later"), nil
		case errors.Is(err, workflow.ErrQueueClosed):
			return mcpgo.NewToolResultErrorf("event queue closed"), nil
		case err != nil:
			return mcpgo.NewToolResultErrorf("retry runner %d: %v", id, err), nil
		}
		return jsonResult(map[string]any{"new_id": newID})
	}
}

// mcpInt reads an optional numeric argument with a default. Returns the
// default on any parse failure or when the argument is absent.
func mcpInt(req mcpgo.CallToolRequest, key string, def int) int {
	v := req.GetFloat(key, float64(def))
	return int(v)
}

// mcpRequiredInt64 reads a required numeric argument. Returns (0, false)
// when the argument is missing or non-numeric so the caller can return
// an explicit "id is required" error.
func mcpRequiredInt64(req mcpgo.CallToolRequest, key string) (int64, bool) {
	v, err := req.RequireFloat(key)
	if err != nil {
		return 0, false
	}
	return int64(v), true
}
