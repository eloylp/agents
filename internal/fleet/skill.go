package fleet

// Skill is a reusable block of guidance that agents can compose. Empty
// WorkspaceID and Repo mean globally visible; WorkspaceID with empty Repo means
// visible only in that workspace; WorkspaceID plus Repo means visible only for
// that repo in the workspace.
type Skill struct {
	ID          string `yaml:"id,omitempty" json:"id,omitempty"`
	WorkspaceID string `yaml:"workspace_id,omitempty" json:"workspace_id,omitempty"`
	Repo        string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Name        string `yaml:"name,omitempty" json:"name,omitempty"`
	Prompt      string `yaml:"prompt" json:"prompt"`
}
