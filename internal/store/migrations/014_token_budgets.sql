CREATE TABLE IF NOT EXISTS token_budgets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_kind   TEXT    NOT NULL DEFAULT 'global',
    scope_name   TEXT    NOT NULL DEFAULT '',
    period       TEXT    NOT NULL DEFAULT 'daily',
    cap_tokens   INTEGER NOT NULL DEFAULT 0,
    alert_at_pct INTEGER NOT NULL DEFAULT 80,
    enabled      INTEGER NOT NULL DEFAULT 1,
    UNIQUE(scope_kind, scope_name, period)
);
