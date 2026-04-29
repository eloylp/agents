// Package server holds the central HTTP server and the cross-cutting types
// that domain handler packages (fleet, repos, observe, config) depend on.
//
// The interfaces here exist for one of two reasons: either they enable
// dependency inversion the import graph requires (HandlerRegister,
// WriteCoordinator, OrphansSource), or they let tests substitute stubs for
// runtime collaborators (StatusProvider, RuntimeStateProvider,
// DispatchStatsProvider, CronReloader, MemoryReader). Production callers
// supply the concrete *scheduler.Scheduler / *observe.Store /
// *workflow.Engine that satisfy the runtime-collaborator interfaces.
package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/scheduler"
	"github.com/eloylp/agents/internal/workflow"
)

// CronReloader is implemented by *scheduler.Scheduler. It is called after a
// CRUD write to update the scheduler's in-process state without restarting
// the daemon. Kept as an interface so crud_test.go can inject an
// errCronReloader stub to exercise reload-failure paths.
type CronReloader interface {
	Reload(repos []fleet.Repo, agents []fleet.Agent, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error
}

// StatusProvider reports the current scheduling state of cron-bound agents.
// /status, /agents (fleet view), and the observability handlers consume it.
// Kept as an interface so tests can stub controlled scheduling state without
// constructing a real *scheduler.Scheduler.
type StatusProvider interface {
	AgentStatuses() []scheduler.AgentStatus
}

// DispatchStatsProvider reports aggregate dispatch statistics for /status
// and /dispatches. Kept as an interface so tests can stub controlled
// counters without constructing a real *workflow.Engine.
type DispatchStatsProvider interface {
	DispatchStats() workflow.DispatchStats
}

// RuntimeStateProvider reports whether a named agent currently has an
// in-flight run. Used by the /agents fleet view and the observability graph.
// Kept as an interface so tests can stub controlled running state without
// constructing a real *observe.Store.
type RuntimeStateProvider interface {
	IsRunning(agentName string) bool
}

// HandlerRegister is the shape every domain handler package satisfies so
// the composing server can mount its routes uniformly. fleet, repos,
// config, observe, and the GitHub webhook handler each provide a concrete
// *Handler that implements RegisterRoutes with this signature.
type HandlerRegister interface {
	RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler)
}

// OrphansSnapshot is the cross-package summary the /status endpoint surfaces
// for the orphan cache. It mirrors the shape of fleet.OrphanedAgentsSnapshot
// without dragging the fleet package into the composing server's import
// graph; the fleet handler adapts its concrete type to this one via a small
// bridge constructed by cmd/agents.
type OrphansSnapshot struct {
	GeneratedAt time.Time
	Count       int
}

// OrphansSource is implemented by the fleet handler. The composing server
// queries it during /status assembly.
type OrphansSource interface {
	OrphansSnapshot() OrphansSnapshot
	RefreshOrphansFromDB() (OrphansSnapshot, error)
}

// WriteCoordinator runs a CRUD write under the same lock that protects the
// server's "DB write → snapshot read → in-memory reload" sequence and, on
// success, triggers the reload step. Domain handler packages (fleet, repos,
// config) call Do for every mutation so all writes share the lock the
// composing server owns — cross-domain writes never observe a torn snapshot.
//
// fn is the storage write. If it returns an error, the reload step is
// skipped and that error is propagated. If fn succeeds, the implementation
// invokes its registered reload hook; any reload error replaces the (nil)
// fn error so callers see the failure that actually prevented hot-reload.
type WriteCoordinator interface {
	Do(fn func() error) error
}

// ErrMemoryNotFound is returned by MemoryReader.ReadMemory when no memory
// record exists for the requested (agent, repo) pair. Callers should use
// errors.Is to distinguish a missing record (404) from a genuine I/O error.
var ErrMemoryNotFound = errors.New("server: memory not found")

// MemoryReader retrieves the stored memory for an (agent, repo) pair. Kept
// as an interface so tests can stub controlled (agent, repo) → content
// mappings instead of constructing a SQLite-backed reader.
//
// ReadMemory returns ErrMemoryNotFound when the record does not exist; it
// returns ("", time.Time{}, nil) when the record exists but the content is
// empty. The returned time.Time is the last-updated timestamp used to set
// the X-Memory-Mtime response header; a zero value means the timestamp is
// unknown.
type MemoryReader interface {
	ReadMemory(agent, repo string) (string, time.Time, error)
}
