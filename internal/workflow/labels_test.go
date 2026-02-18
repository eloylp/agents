package workflow

import "testing"

func TestParseRefineLabel(t *testing.T) {
	tests := []struct {
		label   string
		backend string
		ok      bool
	}{
		{label: "ai:refine", ok: true},
		{label: "ai:refine:codex", backend: "codex", ok: true},
		{label: "ai:review", ok: false},
		{label: "ai:refine:", ok: false},
		{label: "unrelated", ok: false},
	}

	for _, tt := range tests {
		backend, ok := ParseRefineLabel(tt.label)
		if ok != tt.ok {
			t.Fatalf("ParseRefineLabel(%q) ok mismatch: got %v want %v", tt.label, ok, tt.ok)
		}
		if !tt.ok {
			continue
		}
		if backend != tt.backend {
			t.Fatalf("ParseRefineLabel(%q) backend = %q, want %q", tt.label, backend, tt.backend)
		}
	}
}

func TestParseReviewLabel(t *testing.T) {
	tests := []struct {
		label   string
		backend string
		agent   string
		ok      bool
	}{
		{label: "ai:review", agent: "all", ok: true},
		{label: "ai:review:claude", backend: "claude", agent: "all", ok: true},
		{label: "ai:review:claude:security", backend: "claude", agent: "security", ok: true},
		{label: "ai:review:claude:all", backend: "claude", agent: "all", ok: true},
		{label: "ai:review:claude:", ok: false},
		{label: "ai:review:", ok: false},
		{label: "ai:refine", ok: false},
		{label: "unrelated", ok: false},
	}

	for _, tt := range tests {
		backend, agent, ok := ParseReviewLabel(tt.label)
		if ok != tt.ok {
			t.Fatalf("ParseReviewLabel(%q) ok mismatch: got %v want %v", tt.label, ok, tt.ok)
		}
		if !tt.ok {
			continue
		}
		if backend != tt.backend || agent != tt.agent {
			t.Fatalf("ParseReviewLabel(%q) = (%q,%q), want (%q,%q)", tt.label, backend, agent, tt.backend, tt.agent)
		}
	}
}
