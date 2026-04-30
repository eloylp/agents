package fleet

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// OrphanedAgent describes an agent whose pinned model no longer exists in
// the backend model catalog stored in the database.
type OrphanedAgent struct {
	Name            string   `json:"name"`
	Backend         string   `json:"backend"`
	Model           string   `json:"model"`
	Repos           []string `json:"repos,omitempty"`
	AvailableModels []string `json:"available_models,omitempty"`
}

// OrphanedAgentsResponse is the wire shape for /agents/orphans/status.
type OrphanedAgentsResponse struct {
	Count  int             `json:"count"`
	Agents []OrphanedAgent `json:"agents"`
}

// HandleOrphansStatus serves GET /agents/orphans/status. It computes the
// orphan list on the fly from the current SQLite snapshot — there is no
// cache.
func (h *Handler) HandleOrphansStatus(w http.ResponseWriter, _ *http.Request) {
	orphans, err := h.OrphanedAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(OrphanedAgentsResponse{
		Count:  len(orphans),
		Agents: orphans,
	})
}

// OrphanedAgents reads the four entity sets from SQLite and returns the
// list of agents whose pinned model is unavailable in the backend's
// catalog. Callers (the orphan endpoint and /status) re-evaluate on every
// request; there is no cache.
func (h *Handler) OrphanedAgents() ([]OrphanedAgent, error) {
	agents, repos, _, backends, err := store.ReadSnapshot(h.db)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	cfg := &config.Config{
		Agents: agents,
		Repos:  repos,
		Daemon: config.DaemonConfig{AIBackends: backends},
	}
	return computeOrphanedAgents(cfg), nil
}

func computeOrphanedAgents(cfg *config.Config) []OrphanedAgent {
	if cfg == nil {
		return nil
	}

	// Index every repo that references an agent, regardless of repo or
	// binding enabled state. The orphan badge fires on a "model not in
	// the backend catalog" criterion, not on runtime reachability — so
	// the user needs to see every reference that would need fixing (or
	// re-enabling) to clear the orphan, including currently-paused
	// bindings.
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
