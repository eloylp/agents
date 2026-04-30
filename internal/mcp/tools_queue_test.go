package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/eloylp/agents/internal/store"
	"github.com/eloylp/agents/internal/workflow"
)

func TestToolListQueueEventsReturnsRows(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id1, _ := deps.Queue.PushEvent(context.Background(), workflow.Event{
		Kind: "issues.labeled", Number: 1, Repo: workflow.RepoRef{FullName: "owner/one"},
	})
	id2, _ := deps.Queue.PushEvent(context.Background(), workflow.Event{
		Kind: "pull_request.opened", Number: 2, Repo: workflow.RepoRef{FullName: "owner/one"},
	})

	req := mcpgo.CallToolRequest{}
	res, err := toolListQueueEvents(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got struct {
		Events []store.QueueEventRecord `json:"events"`
		Total  int                      `json:"total"`
	}
	decodeText(t, res, &got)
	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	if len(got.Events) != 2 || got.Events[0].ID != id2 || got.Events[1].ID != id1 {
		t.Fatalf("events = %+v, want newest-first [%d %d]", got.Events, id2, id1)
	}
}

func TestToolListQueueEventsRejectsBadStatus(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"status": "garbage"}
	res, err := toolListQueueEvents(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for bad status")
	}
}

func TestToolDeleteQueueEventRemovesRow(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Queue.PushEvent(context.Background(), workflow.Event{Kind: "x"})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolDeleteQueueEvent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	if _, err := deps.Store.GetQueueEvent(id); err == nil {
		t.Errorf("row still present after delete")
	}
}

func TestToolDeleteQueueEventMissingReturnsError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(9999)}
	res, err := toolDeleteQueueEvent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for missing id")
	}
}

func TestToolRetryQueueEventCompleted(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Queue.PushEvent(context.Background(), workflow.Event{
		Kind: "issues.labeled", Number: 7, Repo: workflow.RepoRef{FullName: "owner/one"},
	})
	<-deps.Queue.EventChan()
	_ = deps.Store.MarkEventStarted(id)
	_ = deps.Store.MarkEventCompleted(id)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolRetryQueueEvent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got struct {
		NewID int64 `json:"new_id"`
	}
	decodeText(t, res, &got)
	if got.NewID == 0 || got.NewID == id {
		t.Fatalf("new_id = %d, want a fresh row id distinct from %d", got.NewID, id)
	}
}

func TestToolRetryQueueEventRunning(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Queue.PushEvent(context.Background(), workflow.Event{Kind: "x"})
	<-deps.Queue.EventChan()
	_ = deps.Store.MarkEventStarted(id)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolRetryQueueEvent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for running event")
	}
}

func TestToolRetryQueueEventRequiresID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	res, err := toolRetryQueueEvent(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when id missing")
	}
}
