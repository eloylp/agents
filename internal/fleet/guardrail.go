package fleet

import "strings"

// Guardrail is a named, operator-defined block of policy text that gets
// prepended to every agent's composed prompt at render time. The shipped
// "security" guardrail defends against indirect prompt injection; operators
// can add their own (code style, deployment safety, project norms, etc.)
// without touching code.
type Guardrail struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Content     string `yaml:"content"`
	// Enabled gates rendering. A disabled guardrail stays in the database
	// (so the operator can re-enable later) but is skipped by the renderer.
	Enabled bool `yaml:"enabled"`
	// Position orders rendering. Lower first; ties broken by Name.
	Position int `yaml:"position"`
	// DefaultContent is set only on built-in guardrails shipped with the
	// daemon. The dashboard's "Reset to default" affordance copies it back
	// into Content. Excluded from the YAML import/export shape, the
	// migration is the sole source of truth for built-in defaults, and a
	// re-import must not be able to mutate them.
	DefaultContent string `yaml:"-"`
	// IsBuiltin marks rows that ship with the daemon. Future migrations may
	// update their DefaultContent; operator-added rows are never touched
	// by migrations. Excluded from YAML for the same reason as DefaultContent.
	IsBuiltin bool `yaml:"-"`
}

// NormalizeGuardrailName canonicalises operator-supplied names: lowercase,
// trimmed, internal whitespace collapsed to a single dash. Mirrors the
// normalization applied to skill and agent names.
func NormalizeGuardrailName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	return strings.Join(strings.Fields(name), "-")
}

// NormalizeGuardrail trims fields in place ahead of persistence so the
// stored values are already in canonical form.
func NormalizeGuardrail(g *Guardrail) {
	g.Name = NormalizeGuardrailName(g.Name)
	g.Description = strings.TrimSpace(g.Description)
	g.Content = strings.TrimSpace(g.Content)
	g.DefaultContent = strings.TrimSpace(g.DefaultContent)
}
