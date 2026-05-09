package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/fleet"
)

// ErrMemoryNotFound is returned by MemoryReader.ReadMemory when no row
// exists for the requested (workspace, agent, repo) tuple. Distinct from a row that
// exists with empty content (which returns "" and a nil error) so the
// /memory HTTP endpoint can return 404 for absent entries while still
// returning 200 with an empty body for intentionally-cleared memory.
var ErrMemoryNotFound = errors.New("store: memory not found")

// MemoryBackend is the daemon's read-write memory accessor, satisfying
// the workflow.MemoryBackend interface. The engine uses it during
// runAgent to load and persist each cron / event-driven run's memory
// blob; agent and repo names are normalised so the keys match what the
// /memory HTTP endpoint reads.
type MemoryBackend struct {
	db       *sql.DB
	notifyFn func(workspace, agent, repo string) // optional; called after each successful write
}

// NewMemoryBackend constructs the engine-side memory accessor.
func NewMemoryBackend(db *sql.DB) *MemoryBackend {
	return &MemoryBackend{db: db}
}

// SetChangeNotifier registers a callback invoked after each successful
// write so SSE subscribers (e.g. the dashboard's memory page) see the
// change immediately. Idempotent; setting nil disables notifications.
func (m *MemoryBackend) SetChangeNotifier(fn func(workspace, agent, repo string)) {
	m.notifyFn = fn
}

// ReadMemory reads the persisted memory for (workspace, agent, repo). A missing row
// returns the empty string and nil error, the engine treats that as
// "no prior memory" rather than a hard miss, so first-run agents work.
func (m *MemoryBackend) ReadMemory(workspace, agent, repo string) (string, error) {
	content, _, _, err := ReadMemory(m.db, fleet.NormalizeWorkspaceID(workspace), ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	return content, err
}

// WriteMemory persists the agent's returned memory blob.
func (m *MemoryBackend) WriteMemory(workspace, agent, repo, content string) error {
	workspace = fleet.NormalizeWorkspaceID(workspace)
	a, r := ai.NormalizeToken(agent), ai.NormalizeToken(repo)
	if err := WriteMemory(m.db, workspace, a, r, content); err != nil {
		return err
	}
	if m.notifyFn != nil {
		m.notifyFn(workspace, a, r)
	}
	return nil
}

// MemoryReader is the read-only memory accessor the /memory HTTP endpoint
// consumes. It reports ErrMemoryNotFound for missing rows so the handler
// can return 404, distinguishing "no memory yet" from "intentionally
// cleared". The mtime is returned so the handler can set the
// X-Memory-Mtime response header.
type MemoryReader struct {
	db *sql.DB
}

// NewMemoryReader constructs the HTTP-side read accessor.
func NewMemoryReader(db *sql.DB) *MemoryReader {
	return &MemoryReader{db: db}
}

// ReadMemory returns the memory content + last-updated time for the named
// (workspace, agent, repo) tuple, or ErrMemoryNotFound if no row exists.
func (r *MemoryReader) ReadMemory(workspace, agent, repo string) (string, time.Time, error) {
	content, found, mtime, err := ReadMemory(r.db, fleet.NormalizeWorkspaceID(workspace), ai.NormalizeToken(agent), ai.NormalizeToken(repo))
	if err != nil {
		return "", time.Time{}, err
	}
	if !found {
		return "", time.Time{}, ErrMemoryNotFound
	}
	return content, mtime, nil
}
