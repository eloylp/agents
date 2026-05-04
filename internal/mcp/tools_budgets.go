package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/eloylp/agents/internal/store"
)

func registerBudgetTools(srv *server.MCPServer, deps Deps) {
	if deps.Store == nil {
		return
	}
	srv.AddTool(
		mcpgo.NewTool("list_token_budgets",
			mcpgo.WithDescription("List all token budgets. Each budget caps token usage for a scope (global, backend, or agent) over a UTC calendar period (daily, weekly, monthly)."),
		),
		toolListTokenBudgets(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("create_token_budget",
			mcpgo.WithDescription("Create a token budget. scope_kind: global (all runs), backend (by backend name), agent (by agent name). Periods are UTC calendar windows: daily, weekly (Sunday start), monthly. alert_at_pct (0-100) triggers the NavBar banner when usage reaches that percentage of cap_tokens; 0 disables alerts."),
			mcpgo.WithString("scope_kind",
				mcpgo.Required(),
				mcpgo.Description(`"global", "backend", or "agent".`),
			),
			mcpgo.WithString("scope_name",
				mcpgo.Description("Backend or agent name for non-global scopes. Leave empty for global."),
			),
			mcpgo.WithString("period",
				mcpgo.Required(),
				mcpgo.Description(`"daily", "weekly", or "monthly".`),
			),
			mcpgo.WithNumber("cap_tokens",
				mcpgo.Required(),
				mcpgo.Description("Maximum token count for the scope and period."),
			),
			mcpgo.WithNumber("alert_at_pct",
				mcpgo.Description("Alert threshold 0-100. 0 disables alerts. Defaults to 80."),
			),
			mcpgo.WithBoolean("enabled",
				mcpgo.Description("Whether this budget is active. Defaults to true."),
			),
		),
		toolCreateTokenBudget(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("update_token_budget",
			mcpgo.WithDescription("Partially update a token budget by ID. Only supplied fields are changed; omitted fields are preserved."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Budget ID (from list_token_budgets)."),
			),
			mcpgo.WithString("scope_kind",
				mcpgo.Description(`"global", "backend", or "agent".`),
			),
			mcpgo.WithString("scope_name",
				mcpgo.Description("Backend or agent name for non-global scopes."),
			),
			mcpgo.WithString("period",
				mcpgo.Description(`"daily", "weekly", or "monthly".`),
			),
			mcpgo.WithNumber("cap_tokens",
				mcpgo.Description("Maximum token count."),
			),
			mcpgo.WithNumber("alert_at_pct",
				mcpgo.Description("Alert threshold 0-100."),
			),
			mcpgo.WithBoolean("enabled",
				mcpgo.Description("Whether this budget is active."),
			),
		),
		toolUpdateTokenBudget(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("delete_token_budget",
			mcpgo.WithDescription("Delete a token budget by ID."),
			mcpgo.WithNumber("id",
				mcpgo.Required(),
				mcpgo.Description("Budget ID (from list_token_budgets)."),
			),
		),
		toolDeleteTokenBudget(deps),
	)
	srv.AddTool(
		mcpgo.NewTool("get_token_leaderboard",
			mcpgo.WithDescription("Return per-agent token usage aggregated over a UTC calendar period, ordered by total tokens descending. Each row includes runs and avg_tokens_per_run. Optionally filtered to a single repo. Returns at most 20 rows."),
			mcpgo.WithString("repo",
				mcpgo.Description(`Optional repo full name "owner/repo" to filter to.`),
			),
			mcpgo.WithString("period",
				mcpgo.Description(`"daily", "weekly", or "monthly". Defaults to monthly.`),
			),
		),
		toolGetTokenLeaderboard(deps),
	)
}

func toolListTokenBudgets(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		budgets, err := deps.Store.ListTokenBudgets()
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("list token budgets", err), nil
		}
		if budgets == nil {
			budgets = []store.TokenBudget{}
		}
		return jsonResult(budgets)
	}
}

func toolCreateTokenBudget(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		scopeKind, _ := args["scope_kind"].(string)
		scopeName, _ := args["scope_name"].(string)
		period, _ := args["period"].(string)
		capTokens, _ := args["cap_tokens"].(float64)
		// Use map-key-presence to distinguish "caller omitted" from "caller
		// explicitly passed 0". A zero alert_at_pct disables alerts; omitting
		// the argument defaults to 80.
		alertAtPctRaw, hasAlertAtPct := args["alert_at_pct"]
		var alertAtPct float64 = 80
		if hasAlertAtPct {
			if v, ok := alertAtPctRaw.(float64); ok {
				alertAtPct = v
			}
		}
		enabled, hasEnabled := args["enabled"].(bool)
		if !hasEnabled {
			enabled = true
		}
		b := store.TokenBudget{
			ScopeKind:  scopeKind,
			ScopeName:  scopeName,
			Period:     period,
			CapTokens:  int64(capTokens),
			AlertAtPct: int(alertAtPct),
			Enabled:    enabled,
		}
		created, err := deps.Store.CreateTokenBudget(b)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return jsonResult(created)
	}
}

func toolUpdateTokenBudget(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		id, ok := mcpRequiredInt64(req, "id")
		if !ok {
			return mcpgo.NewToolResultError("id is required"), nil
		}
		var patch store.TokenBudgetPatch
		if v, ok := stringPtrArg(args, "scope_kind"); ok {
			patch.ScopeKind = v
		}
		if v, ok := stringPtrArg(args, "scope_name"); ok {
			patch.ScopeName = v
		}
		if v, ok := stringPtrArg(args, "period"); ok {
			patch.Period = v
		}
		if v, ok, errMsg := intPtrArg(args, "cap_tokens"); ok {
			capTokens := int64(*v)
			patch.CapTokens = &capTokens
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := intPtrArg(args, "alert_at_pct"); ok {
			patch.AlertAtPct = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if v, ok, errMsg := boolPtrArg(args, "enabled"); ok {
			patch.Enabled = v
		} else if errMsg != "" {
			return mcpgo.NewToolResultError(errMsg), nil
		}
		if !patch.AnyFieldSet() {
			return mcpgo.NewToolResultError("at least one field is required"), nil
		}
		updated, err := deps.Store.PatchTokenBudget(id, patch)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return jsonResult(updated)
	}
}

func toolDeleteTokenBudget(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		idF, _ := args["id"].(float64)
		id := int64(idF)
		if err := deps.Store.DeleteTokenBudget(id); err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		return mcpgo.NewToolResultText("deleted"), nil
	}
}

func toolGetTokenLeaderboard(deps Deps) server.ToolHandlerFunc {
	return func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		repo, _ := args["repo"].(string)
		period, _ := args["period"].(string)
		if period == "" {
			period = "monthly"
		}
		entries, err := deps.Store.TokenLeaderboard(repo, period)
		if err != nil {
			return mcpgo.NewToolResultErrorFromErr("token leaderboard", err), nil
		}
		if entries == nil {
			entries = []store.LeaderboardEntry{}
		}
		return jsonResult(entries)
	}
}
