package workflow

import "strings"

func ParseAILabel(label string) (workflow, agent, role string, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(label))
	if normalized == "ai:refine" {
		return workflowIssueRefine, "", "", true
	}
	if strings.HasPrefix(normalized, "ai:refine:") {
		parts := strings.Split(normalized, ":")
		if len(parts) != 3 || parts[2] == "" {
			return "", "", "", false
		}
		return workflowIssueRefine, parts[2], "", true
	}
	if normalized == "ai:review" {
		return workflowPRReview, "", "all", true
	}
	if strings.HasPrefix(normalized, "ai:review:") {
		parts := strings.Split(normalized, ":")
		if len(parts) != 4 || parts[2] == "" || parts[3] == "" {
			return "", "", "", false
		}
		return workflowPRReview, parts[2], parts[3], true
	}
	return "", "", "", false
}
