package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/rs/zerolog"
)

// TokenBudget represents a token cap for a scope over a time period.
type TokenBudget struct {
	ID          int64  `json:"id"`
	ScopeKind   string `json:"scope_kind"`
	ScopeName   string `json:"scope_name,omitempty"` // legacy display/input field
	WorkspaceID string `json:"workspace_id,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Backend     string `json:"backend,omitempty"`
	Period      string `json:"period"` // "daily", "weekly", "monthly"
	CapTokens   int64  `json:"cap_tokens"`
	AlertAtPct  int    `json:"alert_at_pct"` // 0-100; 0 disables alerts
	Enabled     bool   `json:"enabled"`
}

// LeaderboardEntry aggregates token usage for one agent over a period.
type LeaderboardEntry struct {
	Agent            string `json:"agent"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Total            int64  `json:"total"`
	Runs             int64  `json:"runs"`
	AvgTokensPerRun  int64  `json:"avg_tokens_per_run"`
}

// BudgetAlert describes a budget that has reached or exceeded its alert threshold.
type BudgetAlert struct {
	TokenBudget
	UsedTokens int64   `json:"used_tokens"`
	PctUsed    float64 `json:"pct_used"`
}

// BudgetExceededError is returned by CheckBudgets when an enabled cap already
// has usage at or above its limit for the current UTC calendar period.
type BudgetExceededError struct {
	Budget     TokenBudget
	UsedTokens int64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("token budget exceeded: %s scope %q %s cap %d (used %d tokens)", e.Budget.ScopeKind, e.Budget.ScopeName, e.Budget.Period, e.Budget.CapTokens, e.UsedTokens)
}

// TokenBudgetPatch is the partial-update shape used by PATCH surfaces. Nil
// fields are preserved; non-nil fields replace the current value.
type TokenBudgetPatch struct {
	ScopeKind   *string
	ScopeName   *string
	WorkspaceID *string
	Repo        *string
	Agent       *string
	Backend     *string
	Period      *string
	CapTokens   *int64
	AlertAtPct  *int
	Enabled     *bool
}

func (p TokenBudgetPatch) AnyFieldSet() bool {
	return p.ScopeKind != nil ||
		p.ScopeName != nil ||
		p.WorkspaceID != nil ||
		p.Repo != nil ||
		p.Agent != nil ||
		p.Backend != nil ||
		p.Period != nil ||
		p.CapTokens != nil ||
		p.AlertAtPct != nil ||
		p.Enabled != nil
}

func validateBudget(b TokenBudget) error {
	b = normalizeBudget(b)
	if !slices.Contains([]string{"global", "workspace", "repo", "agent", "workspace+repo", "workspace+agent", "workspace+repo+agent", "backend", "workspace+backend"}, b.ScopeKind) {
		return &ErrValidation{Msg: fmt.Sprintf("invalid scope_kind %q", b.ScopeKind)}
	}
	if !slices.Contains([]string{"daily", "weekly", "monthly"}, b.Period) {
		return &ErrValidation{Msg: fmt.Sprintf("invalid period %q: must be one of daily, weekly, monthly", b.Period)}
	}
	switch b.ScopeKind {
	case "global":
	case "workspace":
		if b.WorkspaceID == "" {
			return &ErrValidation{Msg: "workspace_id is required for workspace scope"}
		}
	case "repo":
		if b.Repo == "" {
			return &ErrValidation{Msg: "repo is required for repo scope"}
		}
	case "agent":
		if b.Agent == "" {
			return &ErrValidation{Msg: "agent is required for agent scope"}
		}
	case "backend":
		if b.Backend == "" {
			return &ErrValidation{Msg: "backend is required for backend scope"}
		}
	case "workspace+repo":
		if b.WorkspaceID == "" || b.Repo == "" {
			return &ErrValidation{Msg: "workspace_id and repo are required for workspace+repo scope"}
		}
	case "workspace+agent":
		if b.WorkspaceID == "" || b.Agent == "" {
			return &ErrValidation{Msg: "workspace_id and agent are required for workspace+agent scope"}
		}
	case "workspace+backend":
		if b.WorkspaceID == "" || b.Backend == "" {
			return &ErrValidation{Msg: "workspace_id and backend are required for workspace+backend scope"}
		}
	case "workspace+repo+agent":
		if b.WorkspaceID == "" || b.Repo == "" || b.Agent == "" {
			return &ErrValidation{Msg: "workspace_id, repo, and agent are required for workspace+repo+agent scope"}
		}
	}
	if b.CapTokens <= 0 {
		return &ErrValidation{Msg: "cap_tokens must be greater than 0"}
	}
	if b.AlertAtPct < 0 || b.AlertAtPct > 100 {
		return &ErrValidation{Msg: "alert_at_pct must be between 0 and 100"}
	}
	return nil
}

func normalizeBudget(b TokenBudget) TokenBudget {
	b.ScopeKind = strings.TrimSpace(b.ScopeKind)
	b.ScopeName = strings.TrimSpace(b.ScopeName)
	b.WorkspaceID = strings.TrimSpace(b.WorkspaceID)
	if b.WorkspaceID != "" {
		b.WorkspaceID = fleet.NormalizeWorkspaceID(b.WorkspaceID)
	}
	b.Repo = fleet.NormalizeRepoName(b.Repo)
	b.Agent = fleet.NormalizeAgentName(b.Agent)
	b.Backend = fleet.NormalizeBackendName(b.Backend)
	if b.ScopeName != "" {
		switch b.ScopeKind {
		case "workspace":
			if b.WorkspaceID == "" {
				b.WorkspaceID = fleet.NormalizeWorkspaceID(b.ScopeName)
			}
		case "repo":
			if b.Repo == "" {
				b.Repo = fleet.NormalizeRepoName(b.ScopeName)
			}
		case "agent":
			if b.Agent == "" {
				b.Agent = fleet.NormalizeAgentName(b.ScopeName)
			}
		case "backend":
			if b.Backend == "" {
				b.Backend = fleet.NormalizeBackendName(b.ScopeName)
			}
		}
	}
	switch b.ScopeKind {
	case "global":
		b.ScopeName, b.WorkspaceID, b.Repo, b.Agent, b.Backend = "", "", "", "", ""
	case "workspace":
		b.ScopeName = b.WorkspaceID
		b.Repo, b.Agent, b.Backend = "", "", ""
	case "repo":
		b.ScopeName = b.Repo
		b.WorkspaceID, b.Agent, b.Backend = "", "", ""
	case "agent":
		b.ScopeName = b.Agent
		b.WorkspaceID, b.Repo, b.Backend = "", "", ""
	case "backend":
		b.ScopeName = b.Backend
		b.WorkspaceID, b.Repo, b.Agent = "", "", ""
	case "workspace+repo":
		b.ScopeName = ""
		b.Agent, b.Backend = "", ""
	case "workspace+agent":
		b.ScopeName = ""
		b.Repo, b.Backend = "", ""
	case "workspace+backend":
		b.ScopeName = ""
		b.Repo, b.Agent = "", ""
	case "workspace+repo+agent":
		b.ScopeName = ""
		b.Backend = ""
	default:
		b.ScopeName = ""
	}
	return b
}

func scanTokenBudget(s rowScanner) (TokenBudget, error) {
	var b TokenBudget
	var enabled int
	if err := s.Scan(&b.ID, &b.ScopeKind, &b.ScopeName, &b.WorkspaceID, &b.Repo, &b.Agent, &b.Backend, &b.Period, &b.CapTokens, &b.AlertAtPct, &enabled); err != nil {
		return TokenBudget{}, err
	}
	b.Enabled = intToBool(enabled)
	return normalizeBudget(b), nil
}

// ListTokenBudgets returns all token budgets ordered by scope_kind, scope_name, period.
func ListTokenBudgets(db *sql.DB) ([]TokenBudget, error) {
	rows, err := db.Query(`SELECT id, scope_kind, scope_name, workspace_id, repo, agent, backend, period, cap_tokens, alert_at_pct, enabled FROM token_budgets ORDER BY scope_kind, workspace_id, repo, agent, backend, period`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenBudget
	for rows.Next() {
		b, err := scanTokenBudget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetTokenBudget returns one budget by ID.
func GetTokenBudget(db *sql.DB, id int64) (TokenBudget, error) {
	row := db.QueryRow(`SELECT id, scope_kind, scope_name, workspace_id, repo, agent, backend, period, cap_tokens, alert_at_pct, enabled FROM token_budgets WHERE id = ?`, id)
	b, err := scanTokenBudget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenBudget{}, &ErrNotFound{Msg: fmt.Sprintf("token_budget id=%d not found", id)}
	}
	return b, err
}

// CreateTokenBudget inserts a new budget and returns it with its generated ID.
func CreateTokenBudget(db *sql.DB, b TokenBudget) (TokenBudget, error) {
	b = normalizeBudget(b)
	if err := validateBudget(b); err != nil {
		return TokenBudget{}, err
	}
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM token_budgets WHERE scope_kind=? AND workspace_id=? AND repo=? AND agent=? AND backend=? AND period=?)`,
		b.ScopeKind, b.WorkspaceID, b.Repo, b.Agent, b.Backend, b.Period,
	).Scan(&exists); err != nil {
		return TokenBudget{}, err
	}
	if exists {
		return TokenBudget{}, &ErrConflict{Msg: fmt.Sprintf("token budget for scope_kind=%q scope_name=%q period=%q already exists", b.ScopeKind, b.ScopeName, b.Period)}
	}
	res, err := db.Exec(
		`INSERT INTO token_budgets (scope_kind, scope_name, workspace_id, repo, agent, backend, period, cap_tokens, alert_at_pct, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ScopeKind, b.ScopeName, b.WorkspaceID, b.Repo, b.Agent, b.Backend, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled),
	)
	if err != nil {
		return TokenBudget{}, err
	}
	id, _ := res.LastInsertId()
	b.ID = id
	return b, nil
}

// UpdateTokenBudget replaces all fields of an existing budget.
func UpdateTokenBudget(db *sql.DB, id int64, b TokenBudget) (TokenBudget, error) {
	b = normalizeBudget(b)
	if err := validateBudget(b); err != nil {
		return TokenBudget{}, err
	}
	var conflictID int64
	err := db.QueryRow(
		`SELECT id FROM token_budgets WHERE scope_kind=? AND workspace_id=? AND repo=? AND agent=? AND backend=? AND period=? AND id != ?`,
		b.ScopeKind, b.WorkspaceID, b.Repo, b.Agent, b.Backend, b.Period, id,
	).Scan(&conflictID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TokenBudget{}, err
	}
	if err == nil {
		return TokenBudget{}, &ErrConflict{Msg: fmt.Sprintf("token budget for scope_kind=%q scope_name=%q period=%q already exists", b.ScopeKind, b.ScopeName, b.Period)}
	}
	res, err := db.Exec(
		`UPDATE token_budgets SET scope_kind=?, scope_name=?, workspace_id=?, repo=?, agent=?, backend=?, period=?, cap_tokens=?, alert_at_pct=?, enabled=? WHERE id=?`,
		b.ScopeKind, b.ScopeName, b.WorkspaceID, b.Repo, b.Agent, b.Backend, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled), id,
	)
	if err != nil {
		return TokenBudget{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return TokenBudget{}, &ErrNotFound{Msg: fmt.Sprintf("token_budget id=%d not found", id)}
	}
	b.ID = id
	return b, nil
}

// PatchTokenBudget partially updates an existing budget and returns the
// canonical merged row.
func PatchTokenBudget(db *sql.DB, id int64, patch TokenBudgetPatch) (TokenBudget, error) {
	if !patch.AnyFieldSet() {
		return TokenBudget{}, &ErrValidation{Msg: "at least one field is required"}
	}
	current, err := GetTokenBudget(db, id)
	if err != nil {
		return TokenBudget{}, err
	}
	if patch.ScopeKind != nil {
		current.ScopeKind = *patch.ScopeKind
	}
	if patch.ScopeName != nil {
		current.ScopeName = *patch.ScopeName
	}
	if patch.WorkspaceID != nil {
		current.WorkspaceID = *patch.WorkspaceID
	}
	if patch.Repo != nil {
		current.Repo = *patch.Repo
	}
	if patch.Agent != nil {
		current.Agent = *patch.Agent
	}
	if patch.Backend != nil {
		current.Backend = *patch.Backend
	}
	if patch.Period != nil {
		current.Period = *patch.Period
	}
	if patch.CapTokens != nil {
		current.CapTokens = *patch.CapTokens
	}
	if patch.AlertAtPct != nil {
		current.AlertAtPct = *patch.AlertAtPct
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	return UpdateTokenBudget(db, id, current)
}

// DeleteTokenBudget removes a budget by ID.
func DeleteTokenBudget(db *sql.DB, id int64) error {
	res, err := db.Exec(`DELETE FROM token_budgets WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("token_budget id=%d not found", id)}
	}
	return nil
}

// periodWhereClause returns a SQLite WHERE clause fragment for started_at.
// Boundaries use SQLite's UTC clock: daily starts at 00:00 UTC, weekly starts
// on Sunday 00:00 UTC, and monthly starts at the first day 00:00 UTC.
func periodWhereClause(period string) string {
	switch period {
	case "weekly":
		return "started_at >= datetime('now', 'start of day', '-' || strftime('%w', 'now') || ' days')"
	case "monthly":
		return "started_at >= datetime('now', 'start of month')"
	default: // daily
		return "started_at >= datetime('now', 'start of day')"
	}
}

// TokenUsageFor sums tokens consumed by a scope over a period. Empty filters
// mean "all" for that dimension.
func TokenUsageFor(db *sql.DB, workspaceID, repo, backend, agentName, period string) (int64, error) {
	conditions := []string{periodWhereClause(period)}
	var args []any
	if workspaceID != "" {
		conditions = append(conditions, "workspace_id = ?")
		args = append(args, fleet.NormalizeWorkspaceID(workspaceID))
	}
	if repo != "" {
		conditions = append(conditions, "repo = ?")
		args = append(args, fleet.NormalizeRepoName(repo))
	}
	if backend != "" {
		conditions = append(conditions, "backend = ?")
		args = append(args, fleet.NormalizeBackendName(backend))
	}
	if agentName != "" {
		conditions = append(conditions, "agent = ?")
		args = append(args, fleet.NormalizeAgentName(agentName))
	}
	q := fmt.Sprintf(
		`SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) FROM traces WHERE %s`,
		strings.Join(conditions, " AND "),
	)
	var total int64
	return total, db.QueryRow(q, args...).Scan(&total)
}

// CheckBudgets verifies that no enabled budget cap is exceeded for the given
// backend and agent. Returns nil on any query failure (fail-open so a broken
// token_budgets table never blocks all agent runs).
func CheckBudgets(db *sql.DB, workspaceID, repo, backend, agentName string) error {
	return CheckBudgetsWithLogger(db, workspaceID, repo, backend, agentName, zerolog.Nop())
}

// CheckBudgetsWithLogger is CheckBudgets plus fail-open diagnostics. Query
// errors are logged and ignored so enforcement defects do not halt the fleet.
func CheckBudgetsWithLogger(db *sql.DB, workspaceID, repo, backend, agentName string, logger zerolog.Logger) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	backend = fleet.NormalizeBackendName(backend)
	agentName = fleet.NormalizeAgentName(agentName)
	budgets, err := ListTokenBudgets(db)
	if err != nil {
		logger.Error().Err(err).Str("backend", backend).Str("agent", agentName).Msg("token budget check failed open: list budgets")
		return nil // fail-open
	}
	for _, b := range budgets {
		if !b.Enabled || b.CapTokens <= 0 {
			continue
		}
		// Simple repo/agent/backend budgets are intentionally global across
		// workspaces; use workspace+* kinds for workspace-isolated caps.
		switch b.ScopeKind {
		case "global":
			// always applies
		case "workspace":
			if b.WorkspaceID != workspaceID {
				continue
			}
		case "repo":
			if b.Repo != repo {
				continue
			}
		case "workspace+repo":
			if b.WorkspaceID != workspaceID || b.Repo != repo {
				continue
			}
		case "workspace+agent":
			if b.WorkspaceID != workspaceID || b.Agent != agentName {
				continue
			}
		case "workspace+repo+agent":
			if b.WorkspaceID != workspaceID || b.Repo != repo || b.Agent != agentName {
				continue
			}
		case "workspace+backend":
			if b.WorkspaceID != workspaceID || b.Backend != backend {
				continue
			}
		case "backend":
			if b.Backend != backend {
				continue
			}
		case "agent":
			if b.Agent != agentName {
				continue
			}
		default:
			continue
		}
		var scopeWorkspace, scopeRepo, scopeBackend, scopeAgent string
		switch b.ScopeKind {
		case "workspace":
			scopeWorkspace = b.WorkspaceID
		case "repo":
			scopeRepo = b.Repo
		case "workspace+repo":
			scopeWorkspace, scopeRepo = b.WorkspaceID, b.Repo
		case "workspace+agent":
			scopeWorkspace, scopeAgent = b.WorkspaceID, b.Agent
		case "workspace+repo+agent":
			scopeWorkspace, scopeRepo, scopeAgent = b.WorkspaceID, b.Repo, b.Agent
		case "workspace+backend":
			scopeWorkspace, scopeBackend = b.WorkspaceID, b.Backend
		case "backend":
			scopeBackend = b.Backend
		case "agent":
			scopeAgent = b.Agent
		}
		used, err := TokenUsageFor(db, scopeWorkspace, scopeRepo, scopeBackend, scopeAgent, b.Period)
		if err != nil {
			logger.Error().
				Err(err).
				Int64("budget_id", b.ID).
				Str("scope_kind", b.ScopeKind).
				Str("scope_name", b.ScopeName).
				Str("period", b.Period).
				Msg("token budget check failed open: usage query")
			continue // fail-open per budget
		}
		if used >= b.CapTokens {
			return &BudgetExceededError{Budget: b, UsedTokens: used}
		}
	}
	return nil
}

// BudgetAlerts returns all enabled budgets that have reached or exceeded
// their alert threshold.
func BudgetAlerts(db *sql.DB) ([]BudgetAlert, error) {
	budgets, err := ListTokenBudgets(db)
	if err != nil {
		return nil, err
	}
	var alerts []BudgetAlert
	for _, b := range budgets {
		if !b.Enabled || b.AlertAtPct <= 0 || b.CapTokens <= 0 {
			continue
		}
		var scopeBackend, scopeAgent string
		var scopeWorkspace, scopeRepo string
		switch b.ScopeKind {
		case "global":
			// Empty filters make TokenUsageFor aggregate all workspaces and dimensions.
		case "workspace":
			scopeWorkspace = b.WorkspaceID
		case "repo":
			scopeRepo = b.Repo
		case "workspace+repo":
			scopeWorkspace, scopeRepo = b.WorkspaceID, b.Repo
		case "workspace+agent":
			scopeWorkspace, scopeAgent = b.WorkspaceID, b.Agent
		case "workspace+repo+agent":
			scopeWorkspace, scopeRepo, scopeAgent = b.WorkspaceID, b.Repo, b.Agent
		case "workspace+backend":
			scopeWorkspace, scopeBackend = b.WorkspaceID, b.Backend
		case "backend":
			scopeBackend = b.Backend
		case "agent":
			scopeAgent = b.Agent
		}
		used, err := TokenUsageFor(db, scopeWorkspace, scopeRepo, scopeBackend, scopeAgent, b.Period)
		if err != nil {
			continue
		}
		pct := float64(used) / float64(b.CapTokens) * 100
		if pct >= float64(b.AlertAtPct) {
			alerts = append(alerts, BudgetAlert{
				TokenBudget: b,
				UsedTokens:  used,
				PctUsed:     pct,
			})
		}
	}
	return alerts, nil
}

// TokenLeaderboard returns per-agent token usage aggregated over the given period,
// optionally filtered to a single repo. Ordered by total tokens descending.
func TokenLeaderboard(db *sql.DB, workspaceID, repo, period string) ([]LeaderboardEntry, error) {
	conditions := []string{periodWhereClause(period)}
	var args []any
	if workspaceID != "" {
		conditions = append(conditions, "workspace_id = ?")
		args = append(args, fleet.NormalizeWorkspaceID(workspaceID))
	}
	if repo != "" {
		conditions = append(conditions, "repo = ?")
		args = append(args, fleet.NormalizeRepoName(repo))
	}
	q := fmt.Sprintf(`
		SELECT
			agent,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) AS total,
			COUNT(*) AS runs
		FROM traces
		WHERE %s
		GROUP BY agent
		ORDER BY total DESC
		LIMIT 20
	`, strings.Join(conditions, " AND "))
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaderboardEntry
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.Agent, &e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens, &e.Total, &e.Runs); err != nil {
			return nil, err
		}
		if e.Runs > 0 {
			e.AvgTokensPerRun = e.Total / e.Runs
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// importTokenBudgetsTx upserts token budgets inside an existing transaction.
// In replace mode all existing rows are deleted first; in merge mode existing
// rows (keyed on scope_kind + scope_name + period) are updated in place and
// absent rows are inserted. IDs from the caller are ignored.
func importTokenBudgetsTx(tx *sql.Tx, budgets []TokenBudget, replace bool) error {
	if len(budgets) == 0 && !replace {
		return nil
	}
	if replace {
		if _, err := tx.Exec("DELETE FROM token_budgets"); err != nil {
			return fmt.Errorf("store: import token budgets: truncate: %w", err)
		}
	}
	for _, b := range budgets {
		b = normalizeBudget(b)
		if err := validateBudget(b); err != nil {
			return fmt.Errorf("store: import token budget: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO token_budgets (scope_kind, scope_name, workspace_id, repo, agent, backend, period, cap_tokens, alert_at_pct, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope_kind, workspace_id, repo, agent, backend, period) DO UPDATE SET
				scope_name   = excluded.scope_name,
				cap_tokens   = excluded.cap_tokens,
				alert_at_pct = excluded.alert_at_pct,
				enabled      = excluded.enabled`,
			b.ScopeKind, b.ScopeName, b.WorkspaceID, b.Repo, b.Agent, b.Backend, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled),
		); err != nil {
			return fmt.Errorf("store: import token budget (%s/%s/%s): %w", b.ScopeKind, b.ScopeName, b.Period, err)
		}
	}
	return nil
}

// ImportTokenBudgets upserts token budgets in a standalone transaction.
// In replace mode all existing rows are deleted first; in merge mode existing
// rows (keyed on scope_kind + scope_name + period) are updated in place and
// absent rows are inserted. IDs from the caller are ignored; the database
// assigns them.
func ImportTokenBudgets(db *sql.DB, budgets []TokenBudget, replace bool) error {
	if len(budgets) == 0 && !replace {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: import token budgets: begin: %w", err)
	}
	defer tx.Rollback()
	if err := importTokenBudgetsTx(tx, budgets, replace); err != nil {
		return err
	}
	return tx.Commit()
}
