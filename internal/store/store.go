// Package store is the SQLite-backed persistence boundary for daemon state.
//
// The package owns schema migrations, SQL reads and writes, and atomic import /
// replace transactions for mutable fleet configuration. YAML is only an import /
// export shape; runtime components read durable state from SQLite through the
// typed Store facade and focused package-level helpers.
package store

import (
	"database/sql"
)

// querier is a minimal interface satisfied by both *sql.DB and *sql.Tx,
// allowing load helpers to run inside or outside a transaction without
// duplicating query logic.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type sqlExec interface {
	querier
	Exec(query string, args ...any) (sql.Result, error)
}
