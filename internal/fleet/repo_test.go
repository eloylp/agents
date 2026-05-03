package fleet

import "testing"

func TestValidateRepoName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ok   bool
	}{
		{"eloylp/agents", true},
		{"owner/repo-with-dash", true},
		{"a/b", true},
		{"", false},
		{"   ", false},
		{"single", false},
		{"owner/", false},
		{"/repo", false},
		{"owner/repo/extra", false},
		{"owner repo", false},
		{"owner/re po", false},
		{"owner\trepo", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRepoName(tc.name)
			if tc.ok && err != nil {
				t.Errorf("expected ok, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}
