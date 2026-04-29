package coordinator

import (
	"database/sql"
	"errors"
	"time"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/store"
)

// ErrMemoryNotFound is returned by NewMemoryReader().Read when no row exists
// for the requested (agent, repo) pair. Distinct from a row that exists with
// empty content (which returns "" and a zero error) so /api/memory can
// return 404 for absent entries while still returning 200 with an empty
// body for intentionally-cleared memory.
var ErrMemoryNotFound = errors.New("coordinator: memory not found")

// SQLiteMemory is the daemon's read-write memory backend, satisfying
// workflow.MemoryBackend. The engine uses it during runAgent to load and
// persist each cron / event-driven run's memory blob; agent and repo names
// are normalised with ai.NormalizeToken so the keys match what the
// /api/memory HTTP endpoint reads.
type SQLiteMemory struct {
	db       *sql.DB
	notifyFn func(agent, repo string) // optional; called after each successful write
}

// NewSQLiteMemory constructs the engine-side memory backend.
func NewSQLiteMemory(db *sql.DB) *SQLiteMemory {
	return &SQLiteMemory{db: db}
}

// SetChangeNotifier registers a callback invoked after each successful
// write so SSE subscribers (e.g. the dashboard's memory page) see the
// change immediately. Idempotent; setting nil disables notifications.
func (m *SQLiteMemory) SetChangeNotifier(fn func(agent, repo string)) {
	m.notifyFn = fn
}

// ReadMemory reads the persisted memory for (agent, repo). A missing row
// returns the empty string and nil error — the engine treats that as
// "no prior memory" rather than a hard miss, so first-run agents work.
func (m *SQLiteMemory) ReadMemory(agent, repo string) (string, error) {
	content, _, _, err := store.ReadMemory(m.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	return content, err
}

// WriteMemory persists the agent's returned memory blob.
func (m *SQLiteMemory) WriteMemory(agent, repo, content string) error {
	a, r := ai.NormalizeToken(agent), ai.NormalizeToken(repo)
	if err := store.WriteMemory(m.db, a, r, content); err != nil {
		return err
	}
	if m.notifyFn != nil {
		m.notifyFn(a, r)
	}
	return nil
}

// SQLiteMemoryReader is the read-only memory accessor the /api/memory HTTP
// endpoint consumes. It reports ErrMemoryNotFound for missing rows so the
// handler can return 404, distinguishing "no memory yet" from
// "intentionally cleared". The mtime is returned so the handler can set
// the X-Memory-Mtime response header.
type SQLiteMemoryReader struct {
	db *sql.DB
}

// NewSQLiteMemoryReader constructs the HTTP-side read accessor.
func NewSQLiteMemoryReader(db *sql.DB) *SQLiteMemoryReader {
	return &SQLiteMemoryReader{db: db}
}

// ReadMemory returns the memory content + last-updated time for the named
// (agent, repo) pair, or ErrMemoryNotFound if no row exists.
func (r *SQLiteMemoryReader) ReadMemory(agent, repo string) (string, time.Time, error) {
	content, found, mtime, err := store.ReadMemory(r.db, ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	if err != nil {
		return "", time.Time{}, err
	}
	if !found {
		return "", time.Time{}, ErrMemoryNotFound
	}
	return content, mtime, nil
}
