package fleet

import "strings"

// DefaultWorkspaceID is the compatibility workspace used for installs that
// predate workspace-aware storage and APIs.
const DefaultWorkspaceID = "default"

// NormalizeWorkspaceID trims an optional workspace id and returns Default when
// callers omit it for compatibility with pre-workspace API surfaces.
func NormalizeWorkspaceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return DefaultWorkspaceID
	}
	return id
}

// Workspace is the top-level operational context for repos, agents, runtime
// events, memory, graph layout, and budgets.
type Workspace struct {
	ID          string `yaml:"id,omitempty" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
}

// WorkspaceGuardrailRef is a workspace-local reference to a global guardrail
// catalog entry.
type WorkspaceGuardrailRef struct {
	WorkspaceID   string `yaml:"-" json:"workspace_id,omitempty"`
	GuardrailName string `yaml:"guardrail_name" json:"guardrail_name"`
	Position      int    `yaml:"position" json:"position"`
	Enabled       bool   `yaml:"enabled" json:"enabled"`
}
