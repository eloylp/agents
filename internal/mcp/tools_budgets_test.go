package mcp

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/eloylp/agents/internal/store"
)

func TestToolCreateTokenBudgetAllowsZeroAlert(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"scope_kind":   "global",
		"period":       "daily",
		"cap_tokens":   float64(100),
		"alert_at_pct": float64(0),
	}
	res, err := toolCreateTokenBudget(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("tool err: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	budgets, err := deps.Store.ListTokenBudgets()
	if err != nil {
		t.Fatalf("list budgets: %v", err)
	}
	if len(budgets) != 1 || budgets[0].AlertAtPct != 0 {
		t.Fatalf("budgets = %+v, want alert_at_pct=0", budgets)
	}
}

func TestToolUpdateTokenBudgetIsPartialAndPreservesDisabled(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	created, err := deps.Store.CreateTokenBudget(store.TokenBudget{
		ScopeKind:  "backend",
		ScopeName:  "claude",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 0,
		Enabled:    false,
	})
	if err != nil {
		t.Fatalf("create budget: %v", err)
	}

	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"id":         float64(created.ID),
		"cap_tokens": float64(250),
	}
	res, err := toolUpdateTokenBudget(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("tool err: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	updated, err := deps.Store.GetTokenBudget(created.ID)
	if err != nil {
		t.Fatalf("get budget: %v", err)
	}
	if updated.CapTokens != 250 || updated.Enabled || updated.AlertAtPct != 0 || updated.ScopeName != "claude" {
		t.Fatalf("updated = %+v, want partial patch with disabled preserved", updated)
	}
}

func TestToolUpdateTokenBudgetRejectsEmptyPatch(t *testing.T) {
	t.Parallel()
	deps := testFixture(t)
	created, err := deps.Store.CreateTokenBudget(store.TokenBudget{
		ScopeKind:  "global",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 80,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("create budget: %v", err)
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": float64(created.ID)}
	res, err := toolUpdateTokenBudget(deps)(context.Background(), req)
	if err != nil {
		t.Fatalf("tool err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("tool returned success, want error")
	}
	if got := textOf(t, res); !strings.Contains(got, "at least one field is required") {
		t.Fatalf("error = %q, want empty patch message", got)
	}
}
