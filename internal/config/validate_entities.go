package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

// ValidateCrossRefs checks cross-entity reference consistency across the four
// mutable entity sets. It is called by the SQLite CRUD layer after each write
// (within the same transaction) so that invalid fleet configurations cannot be
// committed to the database.
//
// Specifically it verifies:
//   - every agent references a known backend and known skills
//   - dispatch wiring (can_dispatch) references existing agents with descriptions
//   - every repo binding references a known agent
func ValidateCrossRefs(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend) error {
	agentByName := make(map[string]fleet.Agent, len(agents))
	for _, a := range agents {
		agentByName[a.Name] = a
	}

	// Validate agent → backend and skill references.
	for _, a := range agents {
		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := backends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		if err := validateAgentModel(a.Name, a.Model, backends[a.Backend]); err != nil {
			return err
		}
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
	}

	// Validate can_dispatch wiring.
	for _, a := range agents {
		for _, t := range a.CanDispatch {
			target, ok := agentByName[t]
			if !ok {
				return fmt.Errorf("config: agent %q: can_dispatch references unknown agent %q", a.Name, t)
			}
			if t == a.Name {
				return fmt.Errorf("config: agent %q: can_dispatch must not include itself", a.Name)
			}
			if target.Description == "" {
				return fmt.Errorf("config: agent %q is in a can_dispatch list but has no description (description is required for dispatch targets)", t)
			}
		}
	}

	// Validate repo binding → agent references.
	for _, r := range repos {
		for i, b := range r.Use {
			if _, ok := agentByName[b.Agent]; !ok {
				return fmt.Errorf("config: repo %q: binding #%d references unknown agent %q", r.Name, i, b.Agent)
			}
		}
	}

	return nil
}

// ValidateEntities runs entity-level (non-daemon) invariants on the four
// mutable entity sets. It is a superset of ValidateCrossRefs: it additionally
// checks field-level constraints (non-empty prompts, valid backend names,
// binding trigger types) but does NOT enforce aggregate minimums ("at least one
// agent/repo/backend required") — those are enforced separately on DELETE paths
// so that incremental UPSERT builds remain possible.
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
	for _, a := range agents {
		if a.Name == "" {
			return errors.New("config: agent name is required")
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = struct{}{}
		if a.Backend == "" {
			return fmt.Errorf("config: agent %q: backend is required", a.Name)
		}
		if _, ok := backends[a.Backend]; !ok {
			return fmt.Errorf("config: agent %q: unknown backend %q", a.Name, a.Backend)
		}
		if err := validateAgentModel(a.Name, a.Model, backends[a.Backend]); err != nil {
			return err
		}
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				return fmt.Errorf("config: agent %q: unknown skill %q", a.Name, s)
			}
		}
		if a.Prompt == "" {
			return fmt.Errorf("config: agent %q: prompt is empty", a.Name)
		}
	}
	// Dispatch wiring reuses the Config method which only reads c.Agents.
	if err := (&Config{Agents: agents}).validateDispatchWiring(); err != nil {
		return err
	}

	// Repo binding field checks and agent cross-refs (without "at least one"
	// aggregate check). Agent lookup uses the set built above.
	agentSet := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		agentSet[strings.ToLower(a.Name)] = struct{}{}
	}
	seenRepos := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		if r.Name == "" {
			return errors.New("config: repo name is required")
		}
		key := strings.ToLower(r.Name)
		if _, dup := seenRepos[key]; dup {
			return fmt.Errorf("config: duplicate repo %q", r.Name)
		}
		seenRepos[key] = struct{}{}
		for i, b := range r.Use {
			if b.Agent == "" {
				return fmt.Errorf("config: repo %q: binding #%d has no agent", r.Name, i)
			}
			if _, ok := agentSet[strings.ToLower(b.Agent)]; !ok {
				return fmt.Errorf("config: repo %q: binding references unknown agent %q", r.Name, b.Agent)
			}
			if !b.IsCron() && !b.IsLabel() && !b.IsEvent() {
				return fmt.Errorf("config: repo %q: binding for agent %q has no trigger (set cron, labels, or events)", r.Name, b.Agent)
			}
			if countBindingTriggers(b) > 1 {
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
