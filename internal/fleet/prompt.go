package fleet

// Prompt is a global reusable task/personality contract referenced by
// workspace-local agents.
type Prompt struct {
	ID          string `yaml:"id,omitempty" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
	Content     string `yaml:"content" json:"content"`
}
