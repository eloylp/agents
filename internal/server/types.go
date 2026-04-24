// Package server holds the cross-cutting types that any HTTP-serving
// sub-package needs: runtime status for autonomous agents, dispatch
// counters, event-queue access, memory reads, and the cron-reload hook
// invoked after CRUD writes.
//
// Keeping these definitions in one place lets the domain-scoped server
// packages (fleet, repos, observe, config) depend on a neutral location
// rather than importing each other or the webhook package.
package server

import (
	"context"
	"errors"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/workflow"
)

// CronReloader is implemented by *autonomous.Scheduler. It is called after a
// repo, agent, skill, or backend write to update the scheduler's in-process
// state without restarting the daemon.
type CronReloader interface {
	Reload(repos []config.RepoDef, agents []config.AgentDef, skills map[string]config.SkillDef, backends map[string]config.AIBackendConfig) error
}

// AgentStatus is the runtime state of one autonomous agent as reported by /status.
type AgentStatus struct {
	Name       string     `json:"name"`
	Repo       string     `json:"repo"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	NextRun    time.Time  `json:"next_run"`
	LastStatus string     `json:"last_status,omitempty"`
}

// StatusProvider reports the current scheduling state of autonomous agents.
// The implementation is optional; passing nil results in an empty agents list.
type StatusProvider interface {
	AgentStatuses() []AgentStatus
}

// DispatchStatsProvider reports aggregate dispatch statistics.
// The implementation is optional; passing nil omits the dispatch section.
type DispatchStatsProvider interface {
	DispatchStats() workflow.DispatchStats
}

// RuntimeStateProvider reports whether a named agent currently has an in-flight run.
// The implementation is optional; passing nil causes all agents to report "idle".
type RuntimeStateProvider interface {
	IsRunning(agentName string) bool
}

// EventQueue accepts events for async processing and reports queue depth.
// *workflow.DataChannels satisfies this interface.
type EventQueue interface {
	PushEvent(ctx context.Context, ev workflow.Event) error
	QueueStats() workflow.QueueStat
}

// ErrMemoryNotFound is returned by MemoryReader.ReadMemory when no memory
// record exists for the requested (agent, repo) pair. Callers should use
// errors.Is to distinguish a missing record (404) from a genuine I/O error.
var ErrMemoryNotFound = errors.New("server: memory not found")

// MemoryReader retrieves the stored memory for an (agent, repo) pair.
// The HTTP server uses this interface to serve /api/memory/{agent}/{repo}
// without knowing whether the backing store is the filesystem or SQLite.
// ReadMemory returns ErrMemoryNotFound when the record does not exist; it
// returns ("", time.Time{}, nil) when the record exists but the content is
// empty. The returned time.Time is the last-updated timestamp used to set the
// X-Memory-Mtime response header; a zero value means the timestamp is unknown.
type MemoryReader interface {
	ReadMemory(agent, repo string) (string, time.Time, error)
}
