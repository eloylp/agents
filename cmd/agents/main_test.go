package main

import (
	"path/filepath"
	"testing"
)

func TestAgentsPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		agentsDir  string
		promptFile string
		want       string
	}{
		{
			name:       "relative-path-joined",
			agentsDir:  "/base/agents",
			promptFile: "prompts/review.md",
			want:       "/base/agents/prompts/review.md",
		},
		{
			name:       "absolute-path-unchanged",
			agentsDir:  "/base/agents",
			promptFile: "/etc/custom/PROMPT.md",
			want:       "/etc/custom/PROMPT.md",
		},
		{
			name:       "absolute-path-not-concatenated",
			agentsDir:  "/base/agents",
			promptFile: "/etc/custom/PROMPT.md",
			// filepath.Join would produce /base/agents/etc/custom/PROMPT.md (wrong)
			want: "/etc/custom/PROMPT.md",
		},
		{
			name:       "relative-no-subdir",
			agentsDir:  "/agents",
			promptFile: "PROMPT.md",
			want:       "/agents/PROMPT.md",
		},
		{
			name:       "empty-promptfile-returns-agentsdir",
			agentsDir:  "/agents",
			promptFile: "",
			want:       filepath.Join("/agents", ""),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := agentsPath(tc.agentsDir, tc.promptFile)
			if got != tc.want {
				t.Errorf("agentsPath(%q, %q) = %q, want %q", tc.agentsDir, tc.promptFile, got, tc.want)
			}
		})
	}
}
