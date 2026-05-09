package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eloylp/agents/internal/store"
)

func budgetTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "budgets.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertTraceUsage(t *testing.T, db *sql.DB, spanID, agent, backend, repo string, input, output, cacheRead, cacheWrite int64) {
	t.Helper()
	insertWorkspaceTraceUsage(t, db, "default", spanID, agent, backend, repo, input, output, cacheRead, cacheWrite)
}

func insertWorkspaceTraceUsage(t *testing.T, db *sql.DB, workspaceID, spanID, agent, backend, repo string, input, output, cacheRead, cacheWrite int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO traces (
			span_id, workspace_id, root_event_id, parent_span_id, agent, backend, repo, event_kind,
			started_at, finished_at, duration_ms, status,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		)
		VALUES (?, ?, ?, '', ?, ?, ?, 'issues.labeled', datetime('now'), datetime('now'), 1, 'success', ?, ?, ?, ?)`,
		spanID, workspaceID, "root-"+spanID, agent, backend, repo, input, output, cacheRead, cacheWrite,
	)
	if err != nil {
		t.Fatalf("insert trace usage: %v", err)
	}
}

func TestWorkspaceBudgetsAndLeaderboardFilter(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces(id, name) VALUES('team-a', 'Team A')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := store.CreateTokenBudget(db, store.TokenBudget{
		ScopeKind:   "workspace+agent",
		WorkspaceID: "team-a",
		Agent:       "coder",
		Period:      "daily",
		CapTokens:   40,
		AlertAtPct:  80,
		Enabled:     true,
	}); err != nil {
		t.Fatalf("create workspace budget: %v", err)
	}
	insertTraceUsage(t, db, "default-coder", "coder", "claude", "owner/repo", 30, 20, 0, 0)
	insertWorkspaceTraceUsage(t, db, "team-a", "team-coder", "coder", "claude", "owner/repo", 25, 15, 0, 0)

	if err := store.CheckBudgets(db, "default", "owner/repo", "claude", "coder"); err != nil {
		t.Fatalf("default workspace should not trip team budget: %v", err)
	}
	err := store.CheckBudgets(db, "team-a", "owner/repo", "claude", "coder")
	var exceeded *store.BudgetExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("team workspace budget err = %T %v, want BudgetExceededError", err, err)
	}
	if exceeded.UsedTokens != 40 {
		t.Fatalf("used = %d, want 40", exceeded.UsedTokens)
	}

	rows, err := store.TokenLeaderboard(db, "team-a", "", "daily")
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	if len(rows) != 1 || rows[0].Agent != "coder" || rows[0].Total != 40 {
		t.Fatalf("leaderboard = %+v, want team-a coder total 40", rows)
	}
}

func TestTokenBudgetCompositeScopesClearUnusedFields(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)

	tests := []struct {
		name string
		in   store.TokenBudget
		want store.TokenBudget
	}{
		{
			name: "workspace repo clears agent and backend",
			in: store.TokenBudget{
				ScopeKind:   "workspace+repo",
				WorkspaceID: "Team-A",
				Repo:        "Owner/Repo",
				Agent:       "stale-agent",
				Backend:     "stale-backend",
				Period:      "daily",
				CapTokens:   100,
				Enabled:     true,
			},
			want: store.TokenBudget{WorkspaceID: "Team-A", Repo: "owner/repo"},
		},
		{
			name: "workspace agent clears repo and backend",
			in: store.TokenBudget{
				ScopeKind:   "workspace+agent",
				WorkspaceID: "Team-A",
				Repo:        "stale/repo",
				Agent:       "Coder",
				Backend:     "stale-backend",
				Period:      "daily",
				CapTokens:   100,
				Enabled:     true,
			},
			want: store.TokenBudget{WorkspaceID: "Team-A", Agent: "coder"},
		},
		{
			name: "workspace backend clears repo and agent",
			in: store.TokenBudget{
				ScopeKind:   "workspace+backend",
				WorkspaceID: "Team-A",
				Repo:        "stale/repo",
				Agent:       "stale-agent",
				Backend:     "Claude",
				Period:      "daily",
				CapTokens:   100,
				Enabled:     true,
			},
			want: store.TokenBudget{WorkspaceID: "Team-A", Backend: "claude"},
		},
		{
			name: "workspace repo agent clears backend",
			in: store.TokenBudget{
				ScopeKind:   "workspace+repo+agent",
				WorkspaceID: "Team-A",
				Repo:        "Owner/Repo",
				Agent:       "Coder",
				Backend:     "stale-backend",
				Period:      "daily",
				CapTokens:   100,
				Enabled:     true,
			},
			want: store.TokenBudget{WorkspaceID: "Team-A", Repo: "owner/repo", Agent: "coder"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created, err := store.CreateTokenBudget(db, tc.in)
			if err != nil {
				t.Fatalf("CreateTokenBudget: %v", err)
			}
			if created.ScopeName != "" {
				t.Fatalf("scope_name = %q, want empty composite display field", created.ScopeName)
			}
			if created.WorkspaceID != tc.want.WorkspaceID || created.Repo != tc.want.Repo || created.Agent != tc.want.Agent || created.Backend != tc.want.Backend {
				t.Fatalf("created scope fields = workspace=%q repo=%q agent=%q backend=%q, want workspace=%q repo=%q agent=%q backend=%q",
					created.WorkspaceID, created.Repo, created.Agent, created.Backend,
					tc.want.WorkspaceID, tc.want.Repo, tc.want.Agent, tc.want.Backend)
			}
			if err := store.DeleteTokenBudget(db, created.ID); err != nil {
				t.Fatalf("DeleteTokenBudget: %v", err)
			}
		})
	}
}

func TestTokenBudgetCreatePatchConflictAndValidation(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)

	created, err := store.CreateTokenBudget(db, store.TokenBudget{
		ScopeKind:  "backend",
		ScopeName:  "claude",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 0,
		Enabled:    false,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := store.CreateTokenBudget(db, store.TokenBudget{
		ScopeKind:  "backend",
		ScopeName:  "claude",
		Period:     "daily",
		CapTokens:  200,
		AlertAtPct: 80,
		Enabled:    true,
	}); err == nil {
		t.Fatal("duplicate create: got nil error, want conflict")
	} else {
		var conflict *store.ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("duplicate create: got %T %v, want ErrConflict", err, err)
		}
	}

	newCap := int64(250)
	updated, err := store.PatchTokenBudget(db, created.ID, store.TokenBudgetPatch{CapTokens: &newCap})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.CapTokens != 250 {
		t.Fatalf("cap = %d, want 250", updated.CapTokens)
	}
	if updated.Enabled {
		t.Fatal("patch without enabled re-enabled a disabled budget")
	}
	if updated.AlertAtPct != 0 {
		t.Fatalf("alert_at_pct = %d, want 0 preserved", updated.AlertAtPct)
	}

	if _, err := store.PatchTokenBudget(db, created.ID, store.TokenBudgetPatch{}); err == nil {
		t.Fatal("empty patch: got nil error, want validation")
	} else {
		var validation *store.ErrValidation
		if !errors.As(err, &validation) {
			t.Fatalf("empty patch: got %T %v, want ErrValidation", err, err)
		}
	}
}

func TestTokenBudgetNormalizesLegacyScopeName(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)

	tests := []struct {
		name       string
		budget     store.TokenBudget
		checkRepo  string
		checkBack  string
		checkAgent string
		wantName   string
	}{
		{
			name: "repo",
			budget: store.TokenBudget{
				ScopeKind:  "repo",
				ScopeName:  " Owner/Foo ",
				Period:     "daily",
				CapTokens:  10,
				AlertAtPct: 80,
				Enabled:    true,
			},
			checkRepo:  "OWNER/FOO",
			checkBack:  "claude",
			checkAgent: "coder",
			wantName:   "owner/foo",
		},
		{
			name: "agent",
			budget: store.TokenBudget{
				ScopeKind:  "agent",
				ScopeName:  " Coder ",
				Period:     "daily",
				CapTokens:  10,
				AlertAtPct: 80,
				Enabled:    true,
			},
			checkRepo:  "owner/foo",
			checkBack:  "claude",
			checkAgent: "CODER",
			wantName:   "coder",
		},
		{
			name: "backend",
			budget: store.TokenBudget{
				ScopeKind:  "backend",
				ScopeName:  " Claude ",
				Period:     "daily",
				CapTokens:  10,
				AlertAtPct: 80,
				Enabled:    true,
			},
			checkRepo:  "owner/foo",
			checkBack:  "CLAUDE",
			checkAgent: "coder",
			wantName:   "claude",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created, err := store.CreateTokenBudget(db, tc.budget)
			if err != nil {
				t.Fatalf("create budget: %v", err)
			}
			if created.ScopeName != tc.wantName {
				t.Fatalf("scope_name = %q, want %q", created.ScopeName, tc.wantName)
			}

			insertTraceUsage(t, db, "legacy-"+tc.name, "coder", "claude", "owner/foo", 10, 0, 0, 0)
			err = store.CheckBudgets(db, "default", tc.checkRepo, tc.checkBack, tc.checkAgent)
			var exceeded *store.BudgetExceededError
			if !errors.As(err, &exceeded) {
				t.Fatalf("CheckBudgets err = %T %v, want BudgetExceededError", err, err)
			}

			if err := store.DeleteTokenBudget(db, created.ID); err != nil {
				t.Fatalf("delete budget: %v", err)
			}
		})
	}
}

func TestLegacyStoredBudgetRowsAreNormalizedForChecks(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO token_budgets (scope_kind, scope_name, workspace_id, repo, agent, backend, period, cap_tokens, alert_at_pct, enabled)
		VALUES ('repo', 'Owner/Foo', '', 'Owner/Foo', '', '', 'daily', 10, 80, 1)`,
	); err != nil {
		t.Fatalf("insert legacy budget: %v", err)
	}
	insertTraceUsage(t, db, "legacy-row", "coder", "claude", "owner/foo", 10, 0, 0, 0)

	err := store.CheckBudgets(db, "default", "OWNER/FOO", "CLAUDE", "CODER")
	var exceeded *store.BudgetExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("CheckBudgets err = %T %v, want BudgetExceededError", err, err)
	}
	if exceeded.Budget.ScopeName != "owner/foo" || exceeded.Budget.Repo != "owner/foo" {
		t.Fatalf("budget = %+v, want normalized owner/foo scope", exceeded.Budget)
	}
}

func TestCheckBudgetsExceededAndFailOpen(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)
	if _, err := store.CreateTokenBudget(db, store.TokenBudget{
		ScopeKind:  "global",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 80,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create budget: %v", err)
	}
	insertTraceUsage(t, db, "s1", "coder", "claude", "owner/repo", 60, 40, 0, 0)

	err := store.CheckBudgets(db, "default", "owner/repo", "claude", "coder")
	var exceeded *store.BudgetExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("CheckBudgets err = %T %v, want BudgetExceededError", err, err)
	}
	if exceeded.UsedTokens != 100 {
		t.Fatalf("used = %d, want 100", exceeded.UsedTokens)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := store.CheckBudgets(db, "default", "owner/repo", "claude", "coder"); err != nil {
		t.Fatalf("closed db should fail open, got %v", err)
	}
}

func TestBudgetAlertsAndLeaderboard(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)
	if _, err := store.CreateTokenBudget(db, store.TokenBudget{
		ScopeKind:  "agent",
		ScopeName:  "coder",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 50,
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create budget: %v", err)
	}
	insertTraceUsage(t, db, "coder-1", "coder", "claude", "owner/one", 30, 20, 10, 0)
	insertTraceUsage(t, db, "reviewer-1", "reviewer", "claude", "owner/one", 10, 5, 0, 0)
	insertTraceUsage(t, db, "coder-2", "coder", "claude", "owner/two", 7, 3, 0, 0)

	alerts, err := store.BudgetAlerts(db)
	if err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].ScopeName != "coder" || alerts[0].UsedTokens != 70 {
		t.Fatalf("alerts = %+v, want coder alert at 70 tokens", alerts)
	}

	all, err := store.TokenLeaderboard(db, "", "", "daily")
	if err != nil {
		t.Fatalf("leaderboard all: %v", err)
	}
	if len(all) != 2 || all[0].Agent != "coder" || all[0].Total != 70 || all[0].Runs != 2 || all[0].AvgTokensPerRun != 35 {
		t.Fatalf("leaderboard all = %+v, want coder first with 70 total, 2 runs, 35 avg", all)
	}

	filtered, err := store.TokenLeaderboard(db, "", "owner/two", "daily")
	if err != nil {
		t.Fatalf("leaderboard filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Agent != "coder" || filtered[0].Total != 10 || filtered[0].AvgTokensPerRun != 10 {
		t.Fatalf("leaderboard filtered = %+v, want coder owner/two total 10 avg 10", filtered)
	}
}

func TestTokenLeaderboardLimit(t *testing.T) {
	t.Parallel()
	db := budgetTestDB(t)
	for i := 0; i < 25; i++ {
		agent := "agent-" + string(rune('a'+i))
		insertTraceUsage(t, db, agent, agent, "claude", "owner/repo", int64(i+1), 0, 0, 0)
	}
	rows, err := store.TokenLeaderboard(db, "", "", "daily")
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	if len(rows) != 20 {
		t.Fatalf("len = %d, want 20", len(rows))
	}
}
