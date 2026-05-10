package fleet

// Prompt is a reusable task/personality contract referenced by workspace-local
// agents. Empty WorkspaceID and Repo mean globally visible; WorkspaceID with an
// empty Repo means visible only in that workspace; WorkspaceID plus Repo means
// visible only for that repo in the workspace.
type Prompt struct {
	ID          string `yaml:"id,omitempty" json:"id"`
	WorkspaceID string `yaml:"workspace_id,omitempty" json:"workspace_id,omitempty"`
	Repo        string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
	Content     string `yaml:"content" json:"content"`
}
