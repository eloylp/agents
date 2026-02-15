package workflow

import "strings"

// ParseAILabel maps supported ai:* labels into workflow, agent, and role tokens.
// Supported forms:
// - ai:refine
// - ai:refine:<agent>
// - ai:review
// - ai:review:<agent>:<role>
func ParseAILabel(label string) (workflow, agent, role string, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if workflow, agent, role, ok := parseRefineLabel(normalized); ok {
		return workflow, agent, role, true
	}
	if workflow, agent, role, ok := parseReviewLabel(normalized); ok {
		return workflow, agent, role, true
	}
	return "", "", "", false
}

func parseRefineLabel(normalized string) (workflow, agent, role string, ok bool) {
	if normalized == "ai:refine" {
		return workflowIssueRefine, "", "", true
	}
	if !strings.HasPrefix(normalized, "ai:refine:") {
		return "", "", "", false
	}
	parts := strings.Split(normalized, ":")
	if len(parts) != 3 || parts[2] == "" {
		return "", "", "", false
	}
	return workflowIssueRefine, parts[2], "", true
}

func parseReviewLabel(normalized string) (workflow, agent, role string, ok bool) {
	if normalized == "ai:review" {
		return workflowPRReview, "", "all", true
	}
	if !strings.HasPrefix(normalized, "ai:review:") {
		return "", "", "", false
	}
	parts := strings.Split(normalized, ":")
	if len(parts) != 4 || parts[2] == "" || parts[3] == "" {
		return "", "", "", false
	}
	return workflowPRReview, parts[2], parts[3], true
}
