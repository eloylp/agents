// Package coordinator owns the daemon-wide write epoch: the single mutex
// that serializes every CRUD write across domains, the post-write reload
// chain that propagates the new SQLite state to every component holding
// in-memory copies (engine, scheduler, dispatcher), and the in-memory cfg
// pointer that read paths consume via Config().
//
// The coordinator is constructed once at startup. Every domain handler
// (fleet, repos, config) calls Do(fn) to perform a CRUD write under the
// epoch; on success the registered reload callback runs while the lock is
// still held so no concurrent writer can observe a torn snapshot. The
// in-memory cfg is swapped after the reload callback returns, so handlers
// that call Config() during a reload will block on RLock briefly and then
// see the new value.
//
// Naming: this responsibility used to live on *server.Server as the Do
// method + a CronReloader interface, conflating "the HTTP server's
// lifecycle" with "the daemon's write coherence." The HTTP server is one
// consumer of the coordinator; the actual responsibility is daemon-wide
// and lives here so the cycle between server and its sub-handlers vanishes
// without resorting to local interfaces or function-typed callbacks.
package coordinator

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

// ReloadFunc receives the merged post-write *config.Config and propagates
// it to whatever runtime components hold in-memory copies (typically the
// workflow engine, the dispatcher, and the scheduler). Returning a
// non-nil error fails the whole CRUD write the caller observed — the lock
// is still held when this runs, so a propagation failure leaves the daemon
// in a consistent state and the next successful write will retry the
// reload. A nil ReloadFunc is allowed (e.g. tests that don't wire a
// runtime).
type ReloadFunc func(cfg *config.Config) error

// PostReloadFunc runs after the cfg pointer has been swapped, outside the
// runtime-propagation step. Used for view-layer caches that derive from
// the new cfg (e.g. fleet's orphan cache). May be nil.
type PostReloadFunc func(cfg *config.Config)

// Coordinator owns the write epoch and the current cfg pointer.
type Coordinator struct {
	db *sql.DB

	// storeMu serializes the "CRUD write → snapshot read → in-memory reload"
	// sequence so concurrent writers cannot interleave their snapshots and
	// leave the runtime in a stale or inconsistent state.
	storeMu sync.Mutex

	// cfgMu protects cfg from data races between the reload swap (under
	// storeMu) and concurrent Config() readers. Static daemon fields (HTTP,
	// proxy, log) are never replaced after startup, but since the entire
	// pointer is swapped on reload, the lock is required for all accesses.
	cfgMu sync.RWMutex
	cfg   *config.Config

	reload ReloadFunc
	onPost PostReloadFunc
}

// New builds a Coordinator. cfg is the initial config snapshot; subsequent
// CRUD writes through Do will swap it for fresh snapshots read from db.
// reload propagates each new cfg to the runtime; onPost runs view-layer
// refreshes after the swap. Either may be nil.
func New(cfg *config.Config, db *sql.DB, reload ReloadFunc, onPost PostReloadFunc) *Coordinator {
	return &Coordinator{
		db:     db,
		cfg:    cfg,
		reload: reload,
		onPost: onPost,
	}
}

// DB returns the underlying SQLite handle. Domain handlers use this to run
// store.* mutations inside the fn passed to Do.
func (c *Coordinator) DB() *sql.DB { return c.db }

// Config returns the current effective cfg snapshot under cfgMu.RLock.
// Callers should snapshot once per request handler entry and use the
// returned pointer throughout so they observe a single consistent epoch.
func (c *Coordinator) Config() *config.Config {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// Do runs fn under storeMu and, on success, runs the registered reload
// chain (snapshot → ReloadFunc → cfg swap → PostReloadFunc) while still
// holding the lock. Cross-domain writes therefore share one lock and one
// reload epoch — no concurrent writer can observe a torn snapshot.
//
// fn is the storage write. If fn returns an error, the reload step is
// skipped and that error is propagated. If fn succeeds, a reload error
// replaces the (nil) fn error so callers see the failure that actually
// prevented hot-reload.
func (c *Coordinator) Do(fn func() error) error {
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	return c.runReload()
}

// runReload re-reads the four entity sets in one transaction, propagates
// the merged cfg to the runtime via reload, swaps the in-memory cfg
// pointer, then runs the post-reload hook. Called only from Do (under
// storeMu) and from external recovery paths that need to force a re-sync.
func (c *Coordinator) runReload() error {
	agents, repos, skills, backends, err := store.ReadSnapshot(c.db)
	if err != nil {
		return fmt.Errorf("coordinator: read config snapshot: %w", err)
	}

	// Build the merged cfg from the current snapshot. Daemon-level fields
	// (HTTP, proxy, log) are preserved unchanged — CRUD never touches them.
	c.cfgMu.RLock()
	merged := *c.cfg
	c.cfgMu.RUnlock()
	merged.Repos = repos
	merged.Agents = agents
	merged.Skills = skills
	merged.Daemon.AIBackends = backends

	// Propagate to the runtime first. The engine's UpdateConfigAndRunners
	// is atomic against concurrent runAgent calls; the scheduler's
	// RebuildCron rolls back on failure. After this returns successfully
	// the runtime is on the new epoch.
	if c.reload != nil {
		if err := c.reload(&merged); err != nil {
			return err
		}
	}

	// Swap the cfg pointer so Config() readers (handlers, webhook) see the
	// new state. Done after the runtime is updated so Config() never
	// returns a snapshot the runtime hasn't yet absorbed.
	c.cfgMu.Lock()
	c.cfg = &merged
	c.cfgMu.Unlock()

	if c.onPost != nil {
		c.onPost(&merged)
	}
	return nil
}
