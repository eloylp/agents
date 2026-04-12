package main

import (
	"path/filepath"
	"testing"
)

func TestAgentsPath(t *testing.T) {
	t.Parallel()

	// Use real OS temp dirs so absolute-path assertions are portable across
	// platforms (filepath.IsAbs uses OS-specific rules; hard-coded Unix paths
	// would fail on Windows).
	absBase := t.TempDir()
	absPromptFile := filepath.Join(t.TempDir(), "custom", "PROMPT.md")

	cases := []struct {
		name       string
		agentsDir  string
		promptFile string
		want       string
	}{
		{
			name:       "relative-path-joined",
			agentsDir:  absBase,
			promptFile: filepath.Join("prompts", "review.md"),
			want:       filepath.Join(absBase, "prompts", "review.md"),
		},
		{
			name:       "absolute-path-unchanged",
			agentsDir:  absBase,
			promptFile: absPromptFile,
			want:       absPromptFile,
		},
		{
			name:       "absolute-path-not-concatenated",
			agentsDir:  absBase,
			promptFile: absPromptFile,
			// filepath.Join would absorb the absolute promptFile into agentsDir (wrong)
			want: absPromptFile,
		},
		{
			name:       "relative-no-subdir",
			agentsDir:  absBase,
			promptFile: "PROMPT.md",
			want:       filepath.Join(absBase, "PROMPT.md"),
		},
		{
			name:       "empty-promptfile-returns-agentsdir",
			agentsDir:  absBase,
			promptFile: "",
			want:       filepath.Join(absBase, ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := agentsPath(tc.agentsDir, tc.promptFile)
			if got != tc.want {
				t.Errorf("agentsPath(%q, %q) = %q, want %q", tc.agentsDir, tc.promptFile, got, tc.want)
			}
		})
	}
}
