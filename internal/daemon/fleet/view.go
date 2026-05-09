package fleet

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	fleetcfg "github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/scheduler"
)

// agentScheduleJSON carries scheduling state for cron-backed agents.
type agentScheduleJSON struct {
	LastRun    *string `json:"last_run,omitempty"` // RFC3339 or omitted
	NextRun    string  `json:"next_run"`           // RFC3339
	LastStatus string  `json:"last_status,omitempty"`
}

// agentBindingJSON is the wire shape for one agent-to-repo binding in the
// fleet snapshot view. Schedule is populated only for cron bindings that
// have scheduling state. RepoEnabled mirrors the parent repo's Enabled flag
// so consumers can distinguish "binding itself disabled" from "parent repo
// disabled" without a second fetch against /repos.
type agentBindingJSON struct {
	Repo        string             `json:"repo"`
	RepoEnabled bool               `json:"repo_enabled"`
	Labels      []string           `json:"labels,omitempty"`
	Events      []string           `json:"events,omitempty"`
	Cron        string             `json:"cron,omitempty"`
	Enabled     bool               `json:"enabled"`
	Schedule    *agentScheduleJSON `json:"schedule,omitempty"`
}

// apiAgentJSON is the wire shape for one agent in the GET /agents fleet
// snapshot view. Distinct from storeAgentJSON (the CRUD wire shape), the
// snapshot view exposes runtime status and binding schedules that the CRUD
// representation does not.
type apiAgentJSON struct {
	ID            string             `json:"id"`
	WorkspaceID   string             `json:"workspace_id"`
	Name          string             `json:"name"`
	Backend       string             `json:"backend"`
	Model         string             `json:"model,omitempty"`
	Skills        []string           `json:"skills,omitempty"`
	PromptRef     string             `json:"prompt_ref,omitempty"`
	ScopeType     string             `json:"scope_type,omitempty"`
	ScopeRepo     string             `json:"scope_repo,omitempty"`
	Description   string             `json:"description,omitempty"`
	AllowDispatch bool               `json:"allow_dispatch"`
	CanDispatch   []string           `json:"can_dispatch,omitempty"`
	AllowPRs      bool               `json:"allow_prs"`
	AllowMemory   bool               `json:"allow_memory"`
	CurrentStatus string             `json:"current_status"` // "running" | "idle"
	Bindings      []agentBindingJSON `json:"bindings,omitempty"`
}

// HandleAgentsView serves GET /agents, a fleet snapshot combining agent
// definitions from config with scheduling state from the scheduler. The
// composing daemon's /agents dispatcher routes GET requests here and POST
// requests to HandleAgentsCreate.
func (h *Handler) HandleAgentsView(w http.ResponseWriter, r *http.Request) {
	workspaceID := fleetcfg.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	// Index scheduling state by (workspace, agent, repo) for O(1) lookup below.
	scheduleByKey := map[string]scheduler.AgentStatus{}
	if h.sched != nil {
		for _, st := range h.sched.AgentStatuses() {
			scheduleByKey[fleetcfg.NormalizeWorkspaceID(st.WorkspaceID)+"\x00"+st.Name+"\x00"+st.Repo] = st
		}
	}

	storedAgents, storedRepos, _, _, err := h.store.ReadSnapshot()
	if err != nil {
		http.Error(w, fmt.Sprintf("read snapshot: %v", err), http.StatusInternalServerError)
		return
	}

	// Build one entry per configured agent.
	agents := make([]apiAgentJSON, 0, len(storedAgents))
	for _, a := range storedAgents {
		if fleetcfg.NormalizeWorkspaceID(a.WorkspaceID) != workspaceID {
			continue
		}
		currentStatus := "idle"
		if h.obs != nil && h.obs.IsRunning(a.Name) {
			currentStatus = "running"
		}
		entry := apiAgentJSON{
			ID:            a.ID,
			WorkspaceID:   fleetcfg.NormalizeWorkspaceID(a.WorkspaceID),
			Name:          a.Name,
			Backend:       a.Backend,
			Model:         a.Model,
			Skills:        a.Skills,
			PromptRef:     a.PromptRef,
			ScopeType:     a.ScopeType,
			ScopeRepo:     a.ScopeRepo,
			Description:   a.Description,
			AllowDispatch: a.AllowDispatch,
			CanDispatch:   a.CanDispatch,
			AllowPRs:      a.AllowPRs,
			AllowMemory:   a.IsAllowMemory(),
			CurrentStatus: currentStatus,
		}

		// Collect bindings from all repos that reference this agent,
		// including disabled-repo bindings. Filtering belongs to the consumer:
		// the runtime refuses to dispatch to disabled repos at the boundary,
		// but inspection surfaces (memory page, audit views, MCP) need to see
		// the full configured topology, hiding a binding here also hides the
		// agent's memory in the dashboard, which is exactly the bug we don't
		// want. The repo_enabled flag travels alongside so consumers can mark
		// inactive bindings without a second fetch.
		for _, repo := range storedRepos {
			if fleetcfg.NormalizeWorkspaceID(repo.WorkspaceID) != workspaceID {
				continue
			}
			for _, b := range repo.Use {
				if b.Agent != a.Name {
					continue
				}
				binding := agentBindingJSON{
					Repo:        repo.Name,
					RepoEnabled: repo.Enabled,
					Labels:      b.Labels,
					Events:      b.Events,
					Cron:        b.Cron,
					Enabled:     b.IsEnabled(),
				}
				// Attach scheduling state onto the binding so agents with cron
				// schedules in multiple repos each carry their own schedule data.
				if b.IsCron() {
					if st, ok := scheduleByKey[workspaceID+"\x00"+a.Name+"\x00"+repo.Name]; ok {
						j := &agentScheduleJSON{
							NextRun:    st.NextRun.UTC().Format(time.RFC3339),
							LastStatus: st.LastStatus,
						}
						if st.LastRun != nil {
							lr := st.LastRun.UTC().Format(time.RFC3339)
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
