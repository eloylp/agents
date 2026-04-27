package fleet

// Repo describes a single GitHub repository the daemon operates on and the
// agent bindings declared for it.
type Repo struct {
	Name    string    `yaml:"name"`
	Enabled bool      `yaml:"enabled"`
	Use     []Binding `yaml:"use"`
}
