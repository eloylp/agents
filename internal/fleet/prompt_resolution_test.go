package fleet

import (
	"strings"
	"testing"
)

func TestResolvePromptForAgentRejectsUnresolvablePromptRefs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		prompts []Prompt
		agent   Agent
		repo    string
		wantErr string
	}{
		{
			name: "unknown prompt ref",
			prompts: []Prompt{
				{Name: "reviewer", Content: "Review PRs."},
			},
			agent: Agent{
				Name:        "coder",
				WorkspaceID: DefaultWorkspaceID,
				PromptRef:   "missing",
			},
			repo:    "owner/repo",
			wantErr: `references unknown prompt_ref "missing"`,
		},
		{
			name: "ambiguous visible prompt ref",
			prompts: []Prompt{
				{Name: "shared", Content: "Global prompt."},
				{WorkspaceID: DefaultWorkspaceID, Name: "shared", Content: "Workspace prompt."},
			},
			agent: Agent{
				Name:        "coder",
				WorkspaceID: DefaultWorkspaceID,
				PromptRef:   "shared",
			},
			repo:    "owner/repo",
			wantErr: `ambiguous prompt_ref "shared" in workspace "default"; use prompt_id`,
		},
		{
			name: "prompt ref scoped to another workspace",
			prompts: []Prompt{
				{WorkspaceID: "team-b", Name: "reviewer", Content: "Other workspace prompt."},
			},
			agent: Agent{
				Name:        "coder",
				WorkspaceID: "team-a",
				PromptRef:   "reviewer",
			},
			repo:    "owner/repo",
			wantErr: `references unknown prompt_ref "reviewer"`,
		},
		{
			name: "explicit prompt scope outside agent repo",
			prompts: []Prompt{
				{WorkspaceID: "team-a", Repo: "other/repo", Name: "reviewer", Content: "Other repo prompt."},
			},
			agent: Agent{
				Name:        "coder",
				WorkspaceID: "team-a",
				PromptRef:   "reviewer",
				PromptScope: "team-a/other/repo",
			},
			repo:    "owner/repo",
			wantErr: `references unknown prompt_ref "reviewer"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ResolvePromptForAgent(tc.prompts, tc.agent, tc.repo)
			if err == nil {
				t.Fatalf("ResolvePromptForAgent succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}
