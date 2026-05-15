package workflow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/ai"
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// BuildRoster constructs the dispatch target roster for the current agent.
// Dispatch wiring is the authority: only agents in currentAgent.CanDispatch
// that exist, opt in, and have a description are visible.
func BuildRoster(cfg *config.Config, workspaceID, repoName, currentAgentName string) []ai.RosterEntry {
	repo, ok := cfg.RepoByNameInWorkspace(repoName, workspaceID)
	if !ok {
		return nil
	}
	currentAgent, ok := cfg.AgentByNameInWorkspace(currentAgentName, workspaceID)
	if !ok {
		return nil
	}

	var roster []ai.RosterEntry
	for _, targetName := range currentAgent.CanDispatch {
		target, ok := cfg.AgentByNameInWorkspace(targetName, workspaceID)
		if !ok || !target.AllowDispatch || target.Description == "" || !agentScopeAllowsRepo(target, repo) {
			continue
		}
		roster = append(roster, ai.RosterEntry{
			Name:          target.Name,
			Description:   target.Description,
			Skills:        target.Skills,
			AllowDispatch: true,
		})
	}
	return roster
}

func eventWorkspaceID(ev Event) string {
	return fleet.NormalizeWorkspaceID(ev.WorkspaceID)
}

func dedupRepoKey(workspaceID, repo string) string {
	return fleet.NormalizeWorkspaceID(workspaceID) + "\x00" + repo
}

func agentScopeAllowsRepo(agent fleet.Agent, repo fleet.Repo) bool {
	agentWorkspace := fleet.NormalizeWorkspaceID(agent.WorkspaceID)
	repoWorkspace := fleet.NormalizeWorkspaceID(repo.WorkspaceID)
	if agentWorkspace != repoWorkspace {
		return false
	}

	scopeType := strings.TrimSpace(agent.ScopeType)
	if scopeType == "" {
		scopeType = "workspace"
	}
	switch scopeType {
	case "workspace":
		return strings.TrimSpace(agent.ScopeRepo) == ""
	case "repo":
		return fleet.NormalizeRepoName(agent.ScopeRepo) == repo.Name
	default:
		return false
	}
}

const dynamicWorkspaceGuardrailName = "workspace-boundary"

func dynamicWorkspaceGuardrail(workspaceID string, agent fleet.Agent, repos []fleet.Repo) fleet.Guardrail {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	allowed := allowedReposForAgent(workspaceID, agent, repos)
	var b strings.Builder
	b.WriteString("## Workspace and repository boundaries\n\n")
	fmt.Fprintf(&b, "You are running inside workspace: %s.\n\n", workspaceID)
	b.WriteString("Allowed repositories for this run:\n")
	if len(allowed) == 0 {
		b.WriteString("- (none)\n")
	} else {
		for _, repo := range allowed {
			fmt.Fprintf(&b, "- %s\n", repo)
		}
	}
	b.WriteString("\nYou must not read, write, inspect, clone, modify, comment on, or open pull requests against repositories outside this allow-list.\n\n")
	b.WriteString("If the task appears to require a repository outside this allow-list, abort and explain that the requested repository is outside your workspace/scope boundary.")
	return fleet.Guardrail{
		Name:    dynamicWorkspaceGuardrailName,
		Content: b.String(),
		Enabled: true,
		// The renderer preserves input order today; keep the negative
		// position as a defensive marker if guardrails are sorted later.
		Position: -1,
	}
}

func allowedReposForAgent(workspaceID string, agent fleet.Agent, repos []fleet.Repo) []string {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	scopeType := strings.TrimSpace(agent.ScopeType)
	if scopeType == "" {
		scopeType = "workspace"
	}
	switch scopeType {
	case "workspace", "repo":
	default:
		return nil
	}
	var allowed []string
	for _, repo := range repos {
		repoWorkspace := fleet.NormalizeWorkspaceID(repo.WorkspaceID)
		if repoWorkspace != workspaceID || !repo.Enabled {
			continue
		}
		if scopeType == "repo" && fleet.NormalizeRepoName(agent.ScopeRepo) != repo.Name {
			continue
		}
		allowed = append(allowed, repo.Name)
	}
	return slices.Compact(slices.Sorted(slices.Values(allowed)))
}
