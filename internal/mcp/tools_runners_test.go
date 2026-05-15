package mcp

import (
	"context"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	daemonrunners "github.com/eloylp/agents/internal/daemon/runners"
	"github.com/eloylp/agents/internal/workflow"
)

func TestToolListRunnersReturnsRows(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id1, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-1", Kind: "issues.labeled", Number: 1, Repo: workflow.RepoRef{FullName: "owner/one"},
	})
	id2, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-2", Kind: "pull_request.opened", Number: 2, Repo: workflow.RepoRef{FullName: "owner/one"},
	})

	req := mcpgo.CallToolRequest{}
	res, err := toolListRunners(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got daemonrunners.ListResponse
	decodeText(t, res, &got)
	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	// Both events are in-flight (no traces yet) → 1 row per event with
	// agent empty, status=enqueued.
	if len(got.Runners) != 2 || got.Runners[0].ID != id2 || got.Runners[1].ID != id1 {
		t.Fatalf("runners = %+v, want newest-first [%d %d]", got.Runners, id2, id1)
	}
}

func TestToolListRunnersIncludesTraceError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{
		ID: "ev-timeout", Kind: "issues.labeled", Number: 3, Repo: workflow.RepoRef{FullName: "owner/one"},
	})
	<-deps.Channels.EventChan()
	_ = deps.Store.MarkEventStarted(id)
	now := time.Now()
	deps.Observe.RecordSpan(workflow.SpanInput{
		SpanID: "sp-timeout", RootEventID: "ev-timeout",
		Agent: "coder", Backend: "codex", Repo: "owner/one", EventKind: "issues.labeled",
		Number: 3, Summary: "partial checkpoint",
		StartedAt: now, FinishedAt: now.Add(time.Second),
		Status: "error", ErrorMsg: "parse codex response: empty response (no fields populated)",
		ErrorKind: "backend_auth", ErrorDetail: "Codex auth refresh failed; sign in again",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(deps.Observe.TracesByRootEventID("ev-timeout")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = deps.Store.MarkEventCompleted(id)

	req := mcpgo.CallToolRequest{}
	res, err := toolListRunners(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got daemonrunners.ListResponse
	decodeText(t, res, &got)
	if len(got.Runners) == 0 {
		t.Fatal("no runners returned")
	}
	row := got.Runners[0]
	if row.Status != "error" {
		t.Fatalf("status = %q, want error", row.Status)
	}
	if row.Summary != "partial checkpoint" {
		t.Fatalf("summary = %q, want partial checkpoint", row.Summary)
	}
	if row.Error != "parse codex response: empty response (no fields populated)" {
		t.Fatalf("error = %q, want parser detail", row.Error)
	}
	if row.ErrorKind != "backend_auth" {
		t.Fatalf("error_kind = %q, want backend_auth", row.ErrorKind)
	}
	if row.ErrorDetail != "Codex auth refresh failed; sign in again" {
		t.Fatalf("error_detail = %q, want backend detail", row.ErrorDetail)
	}
}

func TestToolListRunnersRejectsBadStatus(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"status": "garbage"}
	res, err := toolListRunners(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for bad status")
	}
}

func TestToolDeleteRunnerRemovesRow(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{Kind: "x"})

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolDeleteRunner(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	if _, err := deps.Store.GetRunner(id); err == nil {
		t.Errorf("row still present after delete")
	}
}

func TestToolDeleteRunnerMissingReturnsError(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(9999)}
	res, err := toolDeleteRunner(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for missing id")
	}
}

func TestToolRetryRunnerCompleted(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{
		Kind: "issues.labeled", Number: 7, Repo: workflow.RepoRef{FullName: "owner/one"},
	})
	<-deps.Channels.EventChan()
	_ = deps.Store.MarkEventStarted(id)
	_ = deps.Store.MarkEventCompleted(id)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolRetryRunner(deps)(context.Background(), req)
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

func TestToolRetryRunnerRunning(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	id, _ := deps.Channels.PushEvent(context.Background(), workflow.Event{Kind: "x"})
	<-deps.Channels.EventChan()
	_ = deps.Store.MarkEventStarted(id)

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(id)}
	res, err := toolRetryRunner(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for running event")
	}
}

func TestToolRetryRunnerRequiresID(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	res, err := toolRetryRunner(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when id missing")
	}
}
