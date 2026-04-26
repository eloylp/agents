package webhook

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eloylp/agents/internal/config"
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

// OrphanedAgentsSnapshot is the cached orphan-check result used by /status and
// /agents/orphans/status.
type OrphanedAgentsSnapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Count       int             `json:"count"`
	Agents      []OrphanedAgent `json:"agents"`
}

func (s *Server) handleAgentsOrphans(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.orphanedAgentsSnapshot()
	if fresh, err := s.refreshOrphanedAgentsFromDB(); err != nil {
		s.logger.Warn().Err(err).Msg("orphan status: falling back to cached snapshot")
	} else {
		snapshot = fresh
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *Server) refreshOrphanedAgents(cfg *config.Config) {
	snapshot := OrphanedAgentsSnapshot{
		GeneratedAt: time.Now().UTC(),
		Agents:      computeOrphanedAgents(cfg),
	}
	snapshot.Count = len(snapshot.Agents)

	s.orphanMu.Lock()
	s.orphanCache = snapshot
	s.orphanMu.Unlock()
}

func (s *Server) orphanedAgentsSnapshot() OrphanedAgentsSnapshot {
	s.orphanMu.RLock()
	defer s.orphanMu.RUnlock()

	out := s.orphanCache
	out.Agents = append([]OrphanedAgent(nil), s.orphanCache.Agents...)
	return out
}

func (s *Server) refreshOrphanedAgentsFromDB() (OrphanedAgentsSnapshot, error) {
	if s.db == nil {
		return s.orphanedAgentsSnapshot(), nil
	}
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return OrphanedAgentsSnapshot{}, fmt.Errorf("read config snapshot: %w", err)
	}

	baseCfg := s.loadCfg()
	cfg := *baseCfg
	cfg.Agents = agents
	cfg.Repos = repos
	cfg.Skills = skills
	cfg.Daemon.AIBackends = backends

	s.refreshOrphanedAgents(&cfg)
	return s.orphanedAgentsSnapshot(), nil
}

func computeOrphanedAgents(cfg *config.Config) []OrphanedAgent {
	if cfg == nil {
		return nil
	}

	reposByAgent := make(map[string]map[string]struct{})
	for _, repo := range cfg.Repos {
		if !repo.Enabled {
			continue
		}
		for _, binding := range repo.Use {
			if !binding.IsEnabled() {
				continue
			}
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
		if !ok || !config.IsPinnedModelUnavailable(agent.Model, backend) {
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
