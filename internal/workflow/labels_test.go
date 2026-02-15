package workflow

import "testing"

func TestParseAILabel(t *testing.T) {
	tests := []struct {
		label    string
		workflow string
		agent    string
		role     string
		ok       bool
	}{
		{label: "ai:refine", workflow: workflowIssueRefine, ok: true},
		{label: "ai:refine:openai", workflow: workflowIssueRefine, agent: "openai", ok: true},
		{label: "ai:review", workflow: workflowPRReview, role: "all", ok: true},
		{label: "ai:review:claude:security", workflow: workflowPRReview, agent: "claude", role: "security", ok: true},
		{label: "ai:review:claude:all", workflow: workflowPRReview, agent: "claude", role: "all", ok: true},
		{label: "ai:review:claude:", ok: false},
		{label: "unrelated", ok: false},
	}

	for _, tt := range tests {
		workflow, agent, role, ok := ParseAILabel(tt.label)
		if ok != tt.ok {
			t.Fatalf("ParseAILabel(%q) ok mismatch: got %v want %v", tt.label, ok, tt.ok)
		}
		if !tt.ok {
			continue
		}
		if workflow != tt.workflow || agent != tt.agent || role != tt.role {
			t.Fatalf("ParseAILabel(%q) = (%q,%q,%q), want (%q,%q,%q)", tt.label, workflow, agent, role, tt.workflow, tt.agent, tt.role)
		}
	}
}
