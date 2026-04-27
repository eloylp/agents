package fleet

// Skill is a reusable block of guidance that agents can compose.
// After loading, Prompt always contains the resolved guidance text; PromptFile
// is retained only for debugging/logging.
type Skill struct {
	Prompt     string `yaml:"prompt"`
	PromptFile string `yaml:"prompt_file"`
}
