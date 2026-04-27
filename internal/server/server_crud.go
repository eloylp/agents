package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/eloylp/agents/internal/store"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// storeErrStatus maps store mutation errors to the appropriate HTTP status
// code. ErrValidation (bad field values) → 400, ErrConflict (invariant
// violations, referenced-by failures) → 409, ErrNotFound → 404,
// anything else → 500.
func storeErrStatus(err error) int {
	var valErr *store.ErrValidation
	if errors.As(err, &valErr) {
		return http.StatusBadRequest
	}
	var conflictErr *store.ErrConflict
	if errors.As(err, &conflictErr) {
		return http.StatusConflict
	}
	var notFoundErr *store.ErrNotFound
	if errors.As(err, &notFoundErr) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// storeWriteErr maps an error from a store write operation to an HTTP response
// and a structured log entry. op identifies the failing operation (e.g.
// "agent upsert or cron reload") and appears in both the log line and the
// HTTP error body so callers and operators see the same context.
func (s *Server) storeWriteErr(w http.ResponseWriter, err error, op string) {
	s.logger.Error().Err(err).Msgf("store crud: %s failed", op)
	http.Error(w, fmt.Sprintf("%s: %v", op, err), storeErrStatus(err))
}

// reloadCron re-reads the full config from the DB as a consistent snapshot
// and calls Reload on the attached CronReloader (if any). All four entity
// types are read within a single transaction so a concurrent /api/store write
// cannot produce a mixed-epoch snapshot.
//
// MUST be called with s.storeMu held so that no other write can commit and
// re-read a newer snapshot between the point this read begins and the point
// Reload applies the result. Without the lock the "DB commit + snapshot read +
// Reload" sequence is not monotonic: a slow caller can overwrite a newer
// in-memory state that a concurrent faster caller already applied.
func (s *Server) reloadCron() error {
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return fmt.Errorf("read config snapshot for cron reload: %w", err)
	}

	// Reload the scheduler/engine first. If Reload fails we must not update
	// the server's routing config — doing so would leave the daemon split across
	// two config epochs (server on the new snapshot, scheduler/engine on the
	// old one) until the next successful reload or restart.
	if s.cronReloader != nil {
		if err := s.cronReloader.Reload(repos, agents, skills, backends); err != nil {
			return err
		}
	}

	// Reload succeeded: update the server's in-memory routing config so that
	// webhook event handlers (/webhooks/github, /agents/run) and read APIs
	// (/api/agents, /api/config) reflect the post-write state immediately
	// without a restart. Copy-on-write: build a new config value from the
	// current snapshot, replacing only the four CRUD-mutable fields.
	// Daemon-level config (HTTP, proxy, log) is never changed by CRUD writes
	// and is preserved unchanged.
	s.cfgMu.Lock()
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends
	s.cfg = &newCfg
	s.cfgMu.Unlock()
	if s.onConfigReload != nil {
		s.onConfigReload(&newCfg)
	}

	return nil
}
