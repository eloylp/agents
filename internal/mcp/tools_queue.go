package mcp

import (
	"context"
	"errors"
	"net/http/httptest"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	daemonqueue "github.com/eloylp/agents/internal/daemon/queue"
	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

// registerQueueTools wires the /queue surface as MCP tools. Mirrors the
// REST endpoints exposed by internal/daemon/queue: list / delete / retry.
func registerQueueTools(srv *server.MCPServer, deps Deps) {
	if deps.QueueH == nil {
		return
	}
	srv.AddTool(
		mcpgo.NewTool("list_queue_events",
			mcpgo.WithDescription("List rows in the durable event queue with their state (enqueued / running / completed) and timing. Each row carries the kind/repo/number from the original event so the table renders without a second lookup. Same path as GET /queue."),
			mcpgo.WithString("status",
				mcpgo.Description("Optional filter: \"enqueued\", \"running\", or \"completed\". Empty returns every state."),
			),
			mcpgo.WithNumber("limit",
				mcpgo.Description("Maximum rows to return. Defaults to 100."),
			),
			mcpgo.WithNumber("offset",
				mcpgo.Description("Pagination offset (rows skipped from the newest end)."),
			),
		),
		toolListQueueEvents(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("delete_queue_event",
			mcpgo.WithDescription("Remove one row from the event_queue table by id. Best-effort: a worker that has already received the QueuedEvent from the in-memory channel may still run it; the row simply won't appear in /queue afterwards. Same path as DELETE /queue/{id}."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Row id from list_queue_events."),
			),
		),
		toolDeleteQueueEvent(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("retry_queue_event",
			mcpgo.WithDescription("Re-enqueue an event by copying its blob into a fresh event_queue row and pushing onto the channel. The original row stays as audit history. Rejected with a conflict when the source row is in the running state. Same path as POST /queue/{id}/retry."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Row id from list_queue_events to retry."),
			),
		),
		toolRetryQueueEvent(deps),
	)
}

func toolListQueueEvents(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		status, _ := trimmedStringOptional(req, "status")
		limit := mcpInt(req, "limit", 0)
		offset := mcpInt(req, "offset", 0)
		resp, err := deps.QueueH.List(status, limit, offset)
		if err != nil {
			return mcpgo.NewToolResultErrorf("list queue: %v", err), nil
		}
		return jsonResult(resp)
	}
}

func toolDeleteQueueEvent(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := mcpRequiredInt64(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		if err := deps.QueueH.Delete(id); err != nil {
			if errors.Is(err, store.ErrEventNotFound) {
				return mcpgo.NewToolResultErrorf("event %d not found", id), nil
			}
			return mcpgo.NewToolResultErrorf("delete event %d: %v", id, err), nil
		}
		return jsonResult(map[string]any{"deleted": id})
	}
}

func toolRetryQueueEvent(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id, ok := mcpRequiredInt64(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		// Retry needs an *http.Request so it can derive a request context;
		// we use httptest.NewRequest to construct one carrying the MCP
		// tool-call context. No HTTP machinery is invoked beyond that.
		r := httptest.NewRequest("POST", "/queue/0/retry", nil).WithContext(ctx)
		newID, err := deps.QueueH.Retry(r, id)
		switch {
		case errors.Is(err, store.ErrEventNotFound):
			return mcpgo.NewToolResultErrorf("event %d not found", id), nil
		case errors.Is(err, daemonqueue.ErrEventRunning):
			return mcpgo.NewToolResultErrorf("event %d is running and cannot be retried", id), nil
		case errors.Is(err, workflow.ErrEventQueueFull):
			return mcpgo.NewToolResultErrorf("event queue full, retry later"), nil
		case errors.Is(err, workflow.ErrQueueClosed):
			return mcpgo.NewToolResultErrorf("event queue closed"), nil
		case err != nil:
			return mcpgo.NewToolResultErrorf("retry event %d: %v", id, err), nil
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
