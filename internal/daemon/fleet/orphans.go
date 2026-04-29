package fleet

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// OrphanedAgent describes an agent whose pinned model no longer exists in the
// backend model catalog stored in the database snapshot.
type OrphanedAgent struct {
	Name            string   `json:"name"`
	Backend         string   `json:"backend"`
	Model           string   `json:"model"`
	Repos           []string `json:"repos,omitempty"`
	AvailableModels []string `json:"available_models,omitempty"`
}

// OrphanedAgentsSnapshot is the cached orphan-check result used by /status
// and /agents/orphans/status.
type OrphanedAgentsSnapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Count       int             `json:"count"`
	Agents      []OrphanedAgent `json:"agents"`
}

// HandleOrphansStatus serves GET /agents/orphans/status. It refreshes from
// the database snapshot when one is attached so callers see post-write
// state immediately, falling back to the cached cfg-derived snapshot if the
// DB read fails or no database is wired.
func (h *Handler) HandleOrphansStatus(w http.ResponseWriter, _ *http.Request) {
	snapshot := h.OrphansSnapshot()
	if fresh, err := h.RefreshOrphansFromDB(); err != nil {
		h.logger.Warn().Err(err).Msg("orphan status: falling back to cached snapshot")
	} else {
		snapshot = fresh
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// RefreshOrphansFromCfg recomputes the orphan cache from cfg without touching
// the database. The composing daemon calls this once at startup with the
// YAML-loaded config and again on every reloadCron after the in-memory
// config has been swapped to a fresh DB-derived snapshot.
func (h *Handler) RefreshOrphansFromCfg(cfg *config.Config) {
	snap := OrphanedAgentsSnapshot{
		GeneratedAt: time.Now().UTC(),
		Agents:      computeOrphanedAgents(cfg),
	}
	snap.Count = len(snap.Agents)

	h.orphanMu.Lock()
	h.orphanCache = snap
	h.orphanMu.Unlock()
}

// OrphansSnapshot returns a defensive copy of the cached orphan snapshot so
// callers can safely mutate the slice without affecting the cache.
func (h *Handler) OrphansSnapshot() OrphanedAgentsSnapshot {
	h.orphanMu.RLock()
	defer h.orphanMu.RUnlock()
	out := h.orphanCache
	out.Agents = append([]OrphanedAgent(nil), h.orphanCache.Agents...)
	return out
}

// RefreshOrphansFromDB re-reads the four entity sets from SQLite, splices
// them onto the daemon-level config, recomputes orphans, and returns the
// fresh snapshot. When no database is attached it returns the cached
// snapshot unchanged so callers don't need to special-case cfg-only mode.
func (h *Handler) RefreshOrphansFromDB() (OrphanedAgentsSnapshot, error) {
	if h.coord.DB() == nil {
		return h.OrphansSnapshot(), nil
	}
	agents, repos, skills, backends, err := store.ReadSnapshot(h.coord.DB())
	if err != nil {
		return OrphanedAgentsSnapshot{}, fmt.Errorf("read config snapshot: %w", err)
	}

	baseCfg := h.coord.Config()
	cfg := *baseCfg
	cfg.Agents = agents
	cfg.Repos = repos
	cfg.Skills = skills
	cfg.Daemon.AIBackends = backends

	h.RefreshOrphansFromCfg(&cfg)
	return h.OrphansSnapshot(), nil
}

func computeOrphanedAgents(cfg *config.Config) []OrphanedAgent {
	if cfg == nil {
		return nil
	}

	// Index every repo that references an agent, regardless of repo or
	// binding enabled state. The orphan badge fires on a "model not in the
	// backend catalog" criterion, not on runtime reachability — so the user
	// needs to see every reference that would need fixing (or re-enabling)
	// to clear the orphan, including currently-paused bindings.
	reposByAgent := make(map[string]map[string]struct{})
	for _, repo := range cfg.Repos {
		for _, binding := range repo.Use {
			set := reposByAgent[binding.Agent]
			if set == nil {
				set = make(map[string]struct{})
				reposByAgent[binding.Agent] = set
			}
			set[repo.Name] = struct{}{}
		}
	}

	orphan := make([]OrphanedAgent, 0)
	for _, agent := range cfg.Agents {
		backendName := cfg.ResolveBackend(agent.Backend)
		if backendName == "" {
			continue
		}
		backend, ok := cfg.Daemon.AIBackends[backendName]
		if !ok || !fleet.IsPinnedModelUnavailable(agent.Model, backend) {
			continue
		}
		orphan = append(orphan, OrphanedAgent{
			Name:            agent.Name,
			Backend:         backendName,
			Model:           strings.TrimSpace(agent.Model),
			Repos:           slices.Sorted(maps.Keys(reposByAgent[agent.Name])),
			AvailableModels: canonicalModels(backend.Models),
		})
	}

	slices.SortFunc(orphan, func(a, b OrphanedAgent) int {
		if c := strings.Compare(a.Backend, b.Backend); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return orphan
}

func canonicalModels(models []string) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}
