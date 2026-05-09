package fleet

// DefaultWorkspaceID is the compatibility workspace used for installs that
// predate workspace-aware storage and APIs.
const DefaultWorkspaceID = "default"

// Workspace is the top-level operational context for repos, agents, runtime
// events, memory, graph layout, and budgets.
type Workspace struct {
	ID          string `yaml:"id,omitempty" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
}
