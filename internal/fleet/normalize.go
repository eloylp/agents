package fleet

import (
	"slices"
	"strings"
)

// Default values applied to a Backend's optional integer fields when they are
// zero. The same values are used by Config.applyDefaults at startup so that
// CRUD-persisted backends and YAML-loaded backends end up identical.
const (
	DefaultAITimeoutSeconds = 600
	DefaultMaxPromptChars   = 12000
)

func normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// NormalizeAgentName returns the canonical form of an agent name (lowercase,
// trimmed). CRUD callers should normalise path-parameter names before lookups
// or deletes so HTTP routes follow the same case-insensitive semantics as the
// in-memory lookups.
func NormalizeAgentName(name string) string {
	return normalize(name)
}

// NormalizeSkillName returns the canonical form of a skill map key
// (lowercase, trimmed). CRUD callers should normalise the key before writing.
func NormalizeSkillName(name string) string {
	return normalize(name)
}

// NormalizeBackendName returns the canonical form of a backend map key
// (lowercase, trimmed). CRUD callers should normalise the key before writing.
func NormalizeBackendName(name string) string {
	return normalize(name)
}

// NormalizeRepoName returns the canonical form of a repo full name
// (lowercase, trimmed). CRUD callers should normalise path-parameter names
// before lookups or deletes so HTTP routes follow the same case-insensitive
// semantics as the in-memory lookups.
func NormalizeRepoName(name string) string {
	return normalize(name)
}

// NormalizePromptName returns the canonical form of a prompt name
// (lowercase, trimmed). Prompt CRUD follows the same case-insensitive naming
// convention as agents, skills, backends, repos, and guardrails.
func NormalizePromptName(name string) string {
	return normalize(name)
}

// NormalizeAgent applies the same name/field normalization that the YAML
// loader performs at startup (lowercase + trim on names, backend, skills,
// can_dispatch, plus trim on free-text fields). CRUD callers must invoke this
// before writing an agent so the stored values are already in the canonical
// form that lookups expect.
func NormalizeAgent(a *Agent) {
	a.Name = NormalizeAgentName(a.Name)
	a.Backend = NormalizeBackendName(a.Backend)
	a.Model = strings.TrimSpace(a.Model)
	a.Prompt = strings.TrimSpace(a.Prompt)
	a.Description = strings.TrimSpace(a.Description)
	for i := range a.Skills {
		a.Skills[i] = NormalizeSkillName(a.Skills[i])
	}
	for i := range a.CanDispatch {
		a.CanDispatch[i] = NormalizeAgentName(a.CanDispatch[i])
	}
}

// NormalizeSkill applies the same field normalization that the YAML loader
// performs on skill values: trims Prompt.
func NormalizeSkill(s *Skill) {
	s.WorkspaceID = strings.TrimSpace(s.WorkspaceID)
	if s.WorkspaceID != "" {
		s.WorkspaceID = NormalizeWorkspaceID(s.WorkspaceID)
	}
	s.Repo = NormalizeRepoName(s.Repo)
	s.Name = NormalizeSkillName(s.Name)
	s.Prompt = strings.TrimSpace(s.Prompt)
}

// NormalizeBackend applies the same per-entry field normalization that the
// YAML loader performs at startup: trims string fields and each entry of the
// Models slice. CRUD callers must invoke this before persisting a backend so
// the stored values match the canonical form the daemon derives at boot.
func NormalizeBackend(b *Backend) {
	b.Command = strings.TrimSpace(b.Command)
	b.Version = strings.TrimSpace(b.Version)
	b.HealthDetail = strings.TrimSpace(b.HealthDetail)
	b.LocalModelURL = strings.TrimSpace(b.LocalModelURL)
	for i := range b.Models {
		b.Models[i] = strings.TrimSpace(b.Models[i])
	}
}

// NormalizeRepo applies the same normalization that the YAML loader performs
// on repo entries: lowercase+trim the repo name, and lowercase+trim each
// binding agent name, cron, and event strings.
func NormalizeRepo(r *Repo) {
	r.Name = NormalizeRepoName(r.Name)
	for i := range r.Use {
		r.Use[i].Agent = NormalizeAgentName(r.Use[i].Agent)
		r.Use[i].Cron = strings.TrimSpace(r.Use[i].Cron)
		for k := range r.Use[i].Events {
			r.Use[i].Events[k] = normalize(r.Use[i].Events[k])
		}
	}
}

// ApplyBackendDefaults fills in zero-value fields of b with the same defaults
// the YAML loader applies at startup. CRUD callers that persist a backend
// should call this before writing so that the stored values match what the
// daemon would derive from those zeros on the next restart.
func ApplyBackendDefaults(b *Backend) {
	if b.TimeoutSeconds == 0 {
		b.TimeoutSeconds = DefaultAITimeoutSeconds
	}
	if b.MaxPromptChars == 0 {
		b.MaxPromptChars = DefaultMaxPromptChars
	}
}

// IsPinnedModelUnavailable reports whether a non-empty model is explicitly
// known to be unavailable for the given backend snapshot.
//
// If backend.Models is empty, availability is treated as unknown (returns
// false) so callers can avoid blocking on missing catalog metadata.
func IsPinnedModelUnavailable(model string, backend Backend) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if len(backend.Models) == 0 {
		return false
	}
	return !slices.Contains(backend.Models, model)
}
