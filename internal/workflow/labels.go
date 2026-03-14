package workflow

import "strings"

// ParseRefineLabel parses an issue refinement label.
// Supported forms:
//   - ai:refine          (uses default backend)
//   - ai:refine:<backend> (targets a specific backend)
//
// Returns the selected backend (empty for default) and whether the label matched.
func ParseRefineLabel(label string) (backend string, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if normalized == "ai:refine" {
		return "", true
	}
	rest, hasPrefix := strings.CutPrefix(normalized, "ai:refine:")
	if !hasPrefix || rest == "" || strings.Contains(rest, ":") {
		return "", false
	}
	return rest, true
}

// ParseReviewLabel parses a PR review label.
// Supported forms:
//   - ai:review                    (all agents on default backend)
//   - ai:review:<backend>          (all agents on specific backend)
//   - ai:review:<backend>:<agent>  (specific agent on specific backend)
//
// Returns the selected backend (empty for default), agent name, and whether the label matched.
func ParseReviewLabel(label string) (backend, agent string, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if normalized == "ai:review" {
		return "", "all", true
	}
	rest, hasPrefix := strings.CutPrefix(normalized, "ai:review:")
	if !hasPrefix || rest == "" {
		return "", "", false
	}
	backend, agent, hasAgent := strings.Cut(rest, ":")
	if backend == "" {
		return "", "", false
	}
	if !hasAgent {
		return backend, "all", true
	}
	if agent == "" || strings.Contains(agent, ":") {
		return "", "", false
	}
	return backend, agent, true
}
