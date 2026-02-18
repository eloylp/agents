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
	if !strings.HasPrefix(normalized, "ai:refine:") {
		return "", false
	}
	parts := strings.Split(normalized, ":")
	if len(parts) != 3 || parts[2] == "" {
		return "", false
	}
	return parts[2], true
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
	if !strings.HasPrefix(normalized, "ai:review:") {
		return "", "", false
	}
	parts := strings.Split(normalized, ":")
	switch len(parts) {
	case 3:
		if parts[2] == "" {
			return "", "", false
		}
		return parts[2], "all", true
	case 4:
		if parts[2] == "" || parts[3] == "" {
			return "", "", false
		}
		return parts[2], parts[3], true
	default:
		return "", "", false
	}
}
