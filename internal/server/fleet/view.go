package fleet

import (
	"encoding/json"
	"net/http"

	"github.com/eloylp/agents/internal/server"
)

// agentScheduleJSON carries scheduling state for cron-backed agents.
type agentScheduleJSON struct {
	LastRun    *string `json:"last_run,omitempty"` // RFC3339 or omitted
	NextRun    string  `json:"next_run"`           // RFC3339
	LastStatus string  `json:"last_status,omitempty"`
}

// agentBindingJSON is the wire shape for one agent-to-repo binding in the
// fleet snapshot view. Schedule is populated only for cron bindings that
// have scheduling state.
type agentBindingJSON struct {
	Repo     string             `json:"repo"`
	Labels   []string           `json:"labels,omitempty"`
	Events   []string           `json:"events,omitempty"`
	Cron     string             `json:"cron,omitempty"`
	Enabled  bool               `json:"enabled"`
	Schedule *agentScheduleJSON `json:"schedule,omitempty"`
}

// apiAgentJSON is the wire shape for one agent in the GET /agents fleet
// snapshot view. Distinct from storeAgentJSON (the CRUD wire shape) — the
// snapshot view exposes runtime status and binding schedules that the CRUD
// representation does not.
type apiAgentJSON struct {
	Name          string             `json:"name"`
	Backend       string             `json:"backend"`
	Model         string             `json:"model,omitempty"`
	Skills        []string           `json:"skills,omitempty"`
	Description   string             `json:"description,omitempty"`
	AllowDispatch bool               `json:"allow_dispatch"`
	CanDispatch   []string           `json:"can_dispatch,omitempty"`
	AllowPRs      bool               `json:"allow_prs"`
	AllowMemory   bool               `json:"allow_memory"`
	CurrentStatus string             `json:"current_status"` // "running" | "idle"
	Bindings      []agentBindingJSON `json:"bindings,omitempty"`
}

// HandleAgentsView serves GET /agents — a fleet snapshot combining agent
// definitions from config with scheduling state from the StatusProvider.
// The composing server's /agents dispatcher routes GET requests here and
// POST requests to HandleAgentsCreate.
func (h *Handler) HandleAgentsView(w http.ResponseWriter, _ *http.Request) {
	// Index scheduling state by (agent, repo) for O(1) lookup below.
	scheduleByKey := map[string]server.AgentStatus{}
	if h.statusProv != nil {
		for _, st := range h.statusProv.AgentStatuses() {
			scheduleByKey[st.Name+"\x00"+st.Repo] = st
		}
	}

	cfg := h.cfg.Config()

	// Build one entry per configured agent.
	agents := make([]apiAgentJSON, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		currentStatus := "idle"
		if h.runtimeState != nil && h.runtimeState.IsRunning(a.Name) {
			currentStatus = "running"
		}
		entry := apiAgentJSON{
			Name:          a.Name,
			Backend:       a.Backend,
			Model:         a.Model,
			Skills:        a.Skills,
			Description:   a.Description,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
			AllowPRs:      a.AllowPRs,
			AllowMemory:   a.IsAllowMemory(),
			CurrentStatus: currentStatus,
		}

		// Collect bindings from all repos that reference this agent.
		// Disabled repos are excluded entirely — they are not active in the
		// runtime, so they should not appear in the fleet snapshot.
		for _, repo := range cfg.Repos {
			if !repo.Enabled {
				continue
			}
			for _, b := range repo.Use {
				if b.Agent != a.Name {
					continue
				}
				binding := agentBindingJSON{
					Repo:    repo.Name,
					Labels:  b.Labels,
					Events:  b.Events,
					Cron:    b.Cron,
					Enabled: b.IsEnabled(),
				}
				// Attach scheduling state onto the binding so agents with cron
				// schedules in multiple repos each carry their own schedule data.
				if b.IsCron() {
					if st, ok := scheduleByKey[a.Name+"\x00"+repo.Name]; ok {
						j := &agentScheduleJSON{
							NextRun:    st.NextRun.UTC().Format("2006-01-02T15:04:05Z"),
							LastStatus: st.LastStatus,
						}
						if st.LastRun != nil {
							lr := st.LastRun.UTC().Format("2006-01-02T15:04:05Z")
							j.LastRun = &lr
						}
						binding.Schedule = j
					}
				}
				entry.Bindings = append(entry.Bindings, binding)
			}
		}

		agents = append(agents, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agents)
}
