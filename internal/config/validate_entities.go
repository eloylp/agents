package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

func workspaceNameKey(workspaceID, name string) string {
	return fleet.NormalizeWorkspaceID(workspaceID) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

// ValidateEntities runs entity-level (non-daemon) invariants on the four
// mutable entity sets. It checks field-level constraints (non-empty prompts,
// valid backend names, binding trigger types) and cross-entity references, but
// does NOT enforce aggregate minimums ("at least one agent/repo/backend
// required"), those are enforced separately on DELETE paths so that incremental
// UPSERT builds remain possible.
//
// The intent is that every CRUD write on the SQLite store passes ValidateEntities
// so that SQLite is never left in a state that would fail LoadAndValidate on
// restart due to locally invalid entity fields.
func ValidateEntities(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	if backends == nil {
		backends = map[string]fleet.Backend{}
	}
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}

	// Backend field checks (without "at least one" aggregate check).
	for name, b := range backends {
		if !isSupportedBackend(name, b) {
			return fmt.Errorf("config: unsupported ai backend %q (supported: %s, or any custom name with local_model_url set)", name, strings.Join(validAIBackendNames, ", "))
		}
		if b.Command == "" {
			return fmt.Errorf("config: ai backend %q: command is required", name)
		}
	}

	// Skill field checks.
	for name, s := range skills {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: skill name is required")
		}
		if s.Prompt == "" {
			return fmt.Errorf("config: skill %q: prompt is empty", name)
		}
	}

	// Agent field checks, backend/skill cross-refs, and dispatch wiring
	// (without "at least one" aggregate check).
	seen := make(map[string]struct{}, len(agents))
	reposByWorkspace := make(map[string]map[string]struct{})
	for _, r := range repos {
		workspaceID := r.WorkspaceID
		if workspaceID == "" {
			workspaceID = fleet.DefaultWorkspaceID
		}
		if reposByWorkspace[workspaceID] == nil {
			reposByWorkspace[workspaceID] = map[string]struct{}{}
		}
		reposByWorkspace[workspaceID][r.Name] = struct{}{}
	}
	for _, a := range agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		agentKey := workspaceNameKey(a.WorkspaceID, a.Name)
		if _, dup := seen[agentKey]; dup {
			return fmt.Errorf("config: duplicate agent name %q in workspace %q", a.Name, fleet.NormalizeWorkspaceID(a.WorkspaceID))
		}
		seen[agentKey] = struct{}{}
		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := backends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
		if a.Prompt == "" {
			return fmt.Errorf("config: agent %q: prompt is empty", a.Name)
		}
		scopeType := a.ScopeType
		if scopeType == "" {
			scopeType = "workspace"
		}
		switch scopeType {
		case "workspace":
			if a.ScopeRepo != "" {
				return fmt.Errorf("config: agent %q: scope_repo must be empty for workspace scope", a.Name)
			}
		case "repo":
			workspaceID := a.WorkspaceID
			if workspaceID == "" {
				workspaceID = fleet.DefaultWorkspaceID
			}
			if a.ScopeRepo == "" {
				return fmt.Errorf("config: agent %q: scope_repo is required for repo scope", a.Name)
			}
			scopeRepo := fleet.NormalizeRepoName(a.ScopeRepo)
			if _, ok := reposByWorkspace[workspaceID][scopeRepo]; !ok {
				return fmt.Errorf("config: agent %q: scope_repo %q is not a repo in workspace %q", a.Name, a.ScopeRepo, workspaceID)
			}
		default:
			return fmt.Errorf("config: agent %q: unsupported scope_type %q", a.Name, a.ScopeType)
		}
		if a.Description == "" {
			return fmt.Errorf("config: agent %q: description is required (used for agent identification and inter-agent conversations)", a.Name)
		}
	}
	if err := validateDispatchWiring(agents); err != nil {
		return err
	}

	// Repo binding field checks and agent cross-refs (without "at least one"
	// aggregate check). Binding-to-agent lookups reuse seen; agent names are
	// always lowercase-normalised before ValidateEntities is called.
	seenRepos := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		if r.Name == "" {
			return errors.New("config: repo name is required")
		}
		key := workspaceNameKey(r.WorkspaceID, r.Name)
		if _, dup := seenRepos[key]; dup {
			return fmt.Errorf("config: duplicate repo %q in workspace %q", r.Name, fleet.NormalizeWorkspaceID(r.WorkspaceID))
		}
		seenRepos[key] = struct{}{}
		for i, b := range r.Use {
			if b.Agent == "" {
				return fmt.Errorf("config: repo %q: binding #%d has no agent", r.Name, i)
			}
			if _, ok := seen[workspaceNameKey(r.WorkspaceID, b.Agent)]; !ok {
				return fmt.Errorf("config: repo %q: binding references unknown agent %q", r.Name, b.Agent)
			}
			if !b.IsCron() && !b.IsLabel() && !b.IsEvent() {
				return fmt.Errorf("config: repo %q: binding for agent %q has no trigger (set cron, labels, or events)", r.Name, b.Agent)
			}
			if b.TriggerCount() > 1 {
				return fmt.Errorf("config: repo %q: binding for agent %q mixes multiple trigger types (labels, events, cron); each binding must use exactly one trigger", r.Name, b.Agent)
			}
			for _, kind := range b.Events {
				if _, ok := validEventKinds[kind]; !ok {
					return fmt.Errorf("config: repo %q: binding for agent %q has unknown event kind %q (supported: %s)",
						r.Name, b.Agent, kind, strings.Join(validEventKindsSorted, ", "))
				}
			}
		}
	}
	return nil
}
