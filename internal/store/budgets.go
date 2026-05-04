package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TokenBudget represents a token cap for a scope over a time period.
type TokenBudget struct {
	ID         int64  `json:"id"`
	ScopeKind  string `json:"scope_kind"`   // "global", "backend", "agent"
	ScopeName  string `json:"scope_name"`   // "" for global
	Period     string `json:"period"`       // "daily", "weekly", "monthly"
	CapTokens  int64  `json:"cap_tokens"`
	AlertAtPct int    `json:"alert_at_pct"` // 0-100; 0 disables alerts
	Enabled    bool   `json:"enabled"`
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
}

// BudgetAlert describes a budget that has reached or exceeded its alert threshold.
type BudgetAlert struct {
	TokenBudget
	UsedTokens int64   `json:"used_tokens"`
	PctUsed    float64 `json:"pct_used"`
}

func validateBudget(b TokenBudget) error {
	validScope := false
	for _, k := range []string{"global", "backend", "agent"} {
		if b.ScopeKind == k {
			validScope = true
			break
		}
	}
	if !validScope {
		return &ErrValidation{Msg: fmt.Sprintf("invalid scope_kind %q: must be one of global, backend, agent", b.ScopeKind)}
	}
	validPeriod := false
	for _, p := range []string{"daily", "weekly", "monthly"} {
		if b.Period == p {
			validPeriod = true
			break
		}
	}
	if !validPeriod {
		return &ErrValidation{Msg: fmt.Sprintf("invalid period %q: must be one of daily, weekly, monthly", b.Period)}
	}
	if b.ScopeKind != "global" && strings.TrimSpace(b.ScopeName) == "" {
		return &ErrValidation{Msg: "scope_name is required for backend and agent scopes"}
	}
	if b.CapTokens <= 0 {
		return &ErrValidation{Msg: "cap_tokens must be greater than 0"}
	}
	if b.AlertAtPct < 0 || b.AlertAtPct > 100 {
		return &ErrValidation{Msg: "alert_at_pct must be between 0 and 100"}
	}
	return nil
}

func scanTokenBudget(s rowScanner) (TokenBudget, error) {
	var b TokenBudget
	var enabled int
	if err := s.Scan(&b.ID, &b.ScopeKind, &b.ScopeName, &b.Period, &b.CapTokens, &b.AlertAtPct, &enabled); err != nil {
		return TokenBudget{}, err
	}
	b.Enabled = intToBool(enabled)
	return b, nil
}

// ListTokenBudgets returns all token budgets ordered by scope_kind, scope_name, period.
func ListTokenBudgets(db *sql.DB) ([]TokenBudget, error) {
	rows, err := db.Query(`SELECT id, scope_kind, scope_name, period, cap_tokens, alert_at_pct, enabled FROM token_budgets ORDER BY scope_kind, scope_name, period`)
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
	row := db.QueryRow(`SELECT id, scope_kind, scope_name, period, cap_tokens, alert_at_pct, enabled FROM token_budgets WHERE id = ?`, id)
	b, err := scanTokenBudget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenBudget{}, &ErrNotFound{Msg: fmt.Sprintf("token_budget id=%d not found", id)}
	}
	return b, err
}

// CreateTokenBudget inserts a new budget and returns it with its generated ID.
func CreateTokenBudget(db *sql.DB, b TokenBudget) (TokenBudget, error) {
	if b.ScopeKind == "global" {
		b.ScopeName = ""
	}
	if err := validateBudget(b); err != nil {
		return TokenBudget{}, err
	}
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM token_budgets WHERE scope_kind=? AND scope_name=? AND period=?)`,
		b.ScopeKind, b.ScopeName, b.Period,
	).Scan(&exists); err != nil {
		return TokenBudget{}, err
	}
	if exists {
		return TokenBudget{}, &ErrConflict{Msg: fmt.Sprintf("token budget for scope_kind=%q scope_name=%q period=%q already exists", b.ScopeKind, b.ScopeName, b.Period)}
	}
	res, err := db.Exec(
		`INSERT INTO token_budgets (scope_kind, scope_name, period, cap_tokens, alert_at_pct, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		b.ScopeKind, b.ScopeName, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled),
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
	if b.ScopeKind == "global" {
		b.ScopeName = ""
	}
	if err := validateBudget(b); err != nil {
		return TokenBudget{}, err
	}
	var conflictID int64
	err := db.QueryRow(
		`SELECT id FROM token_budgets WHERE scope_kind=? AND scope_name=? AND period=? AND id != ?`,
		b.ScopeKind, b.ScopeName, b.Period, id,
	).Scan(&conflictID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TokenBudget{}, err
	}
	if err == nil {
		return TokenBudget{}, &ErrConflict{Msg: fmt.Sprintf("token budget for scope_kind=%q scope_name=%q period=%q already exists", b.ScopeKind, b.ScopeName, b.Period)}
	}
	res, err := db.Exec(
		`UPDATE token_budgets SET scope_kind=?, scope_name=?, period=?, cap_tokens=?, alert_at_pct=?, enabled=? WHERE id=?`,
		b.ScopeKind, b.ScopeName, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled), id,
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
func periodWhereClause(period string) string {
	switch period {
	case "weekly":
		return "started_at >= datetime('now', '-7 days')"
	case "monthly":
		return "started_at >= datetime('now', 'start of month')"
	default: // daily
		return "started_at >= datetime('now', 'start of day')"
	}
}

// TokenUsageFor sums tokens consumed by a scope over a period.
// backend and agentName are optional filters (empty means all).
func TokenUsageFor(db *sql.DB, backend, agentName, period string) (int64, error) {
	conditions := []string{periodWhereClause(period)}
	var args []any
	if backend != "" {
		conditions = append(conditions, "backend = ?")
		args = append(args, backend)
	}
	if agentName != "" {
		conditions = append(conditions, "agent = ?")
		args = append(args, agentName)
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
func CheckBudgets(db *sql.DB, backend, agentName string) error {
	budgets, err := ListTokenBudgets(db)
	if err != nil {
		return nil // fail-open
	}
	for _, b := range budgets {
		if !b.Enabled || b.CapTokens <= 0 {
			continue
		}
		switch b.ScopeKind {
		case "global":
			// always applies
		case "backend":
			if b.ScopeName != backend {
				continue
			}
		case "agent":
			if b.ScopeName != agentName {
				continue
			}
		default:
			continue
		}
		var scopeBackend, scopeAgent string
		switch b.ScopeKind {
		case "backend":
			scopeBackend = b.ScopeName
		case "agent":
			scopeAgent = b.ScopeName
		}
		used, err := TokenUsageFor(db, scopeBackend, scopeAgent, b.Period)
		if err != nil {
			continue // fail-open per budget
		}
		if used >= b.CapTokens {
			return fmt.Errorf("token budget exceeded: %s scope %q %s cap %d (used %d tokens)", b.ScopeKind, b.ScopeName, b.Period, b.CapTokens, used)
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
		switch b.ScopeKind {
		case "backend":
			scopeBackend = b.ScopeName
		case "agent":
			scopeAgent = b.ScopeName
		}
		used, err := TokenUsageFor(db, scopeBackend, scopeAgent, b.Period)
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
func TokenLeaderboard(db *sql.DB, repo, period string) ([]LeaderboardEntry, error) {
	conditions := []string{periodWhereClause(period)}
	var args []any
	if repo != "" {
		conditions = append(conditions, "repo = ?")
		args = append(args, repo)
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
		if b.ScopeKind == "global" {
			b.ScopeName = ""
		}
		if err := validateBudget(b); err != nil {
			return fmt.Errorf("store: import token budget: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO token_budgets (scope_kind, scope_name, period, cap_tokens, alert_at_pct, enabled)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope_kind, scope_name, period) DO UPDATE SET
				cap_tokens   = excluded.cap_tokens,
				alert_at_pct = excluded.alert_at_pct,
				enabled      = excluded.enabled`,
			b.ScopeKind, b.ScopeName, b.Period, b.CapTokens, b.AlertAtPct, boolToInt(b.Enabled),
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
