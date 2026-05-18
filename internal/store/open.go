package store

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite" // register the sqlite3 driver
)

// Open opens (or creates) a SQLite database at path and runs all pending
// schema migrations. It returns a ready-to-use *sql.DB.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// Use WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: enable foreign keys: %w", err)
	}
	// Retry for up to 5 s when another goroutine holds a write lock instead of
	// returning SQLITE_BUSY immediately. The observe store records spans,
	// events, and dispatches on concurrent goroutines, so without a timeout
	// those writes race and one of them can fail with "database is locked".
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: set busy timeout: %w", err)
	}
	// Pin to a single connection so PRAGMAs set above (busy_timeout, WAL mode,
	// foreign_keys) apply to every subsequent operation. Without this,
	// database/sql may open additional connections that bypass those settings,
	// causing spurious SQLITE_BUSY under concurrent goroutine writes.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
