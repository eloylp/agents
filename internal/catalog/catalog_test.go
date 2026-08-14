package catalog

import (
	"errors"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func TestParseValidatesDelegatedCatalogSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "valid",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    workspace_id: default
    repo: eloylp/agents
    name: coder
    body: write code
  - id: go-api
    kind: skill
    name: go-api
    body: use handlers
  - id: security
    kind: guardrail
    name: security
    enabled: true
    position: 10
    body: do not expose secrets
`,
		},
		{
			name: "duplicate id",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: one
  - id: coder
    kind: skill
    name: coder
    body: two
`,
			wantErr: `duplicate id "coder"`,
		},
		{
			name: "unsupported kind",
			body: `
version: 1
assets:
  - id: workflow
    kind: agent
    name: workflow
    body: no
`,
			wantErr: `unsupported kind "agent"`,
		},
		{
			name: "forbidden workflow field",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    agent: coder
    body: no
`,
			wantErr: `forbidden field "agent"`,
		},
		{
			name: "guardrail only fields",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    enabled: true
    body: no
`,
			wantErr: "enabled and position are only valid for guardrails",
		},
		{
			name: "repo scope requires workspace",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    repo: eloylp/agents
    name: coder
    body: no
`,
			wantErr: "repo scope requires workspace_id",
		},
		{
			name: "guardrail repo scope",
			body: `
version: 1
assets:
  - id: security
    kind: guardrail
    workspace_id: default
    repo: eloylp/agents
    name: security
    body: no
`,
			wantErr: "repo scope is only valid for prompts and skills",
		},
		{
			name: "version suffix",
			body: `
version: 1
assets:
  - id: coder@1
    kind: prompt
    name: coder
    body: no
`,
			wantErr: "must not include a version suffix",
		},
		{
			name: "invalid public ref characters",
			body: `
version: 1
assets:
  - id: Bad/Ref
    kind: prompt
    name: coder
    body: no
`,
			wantErr: "must contain only lowercase letters, digits, hyphen, or underscore",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse() error = %v, want nil", err)
				}
				return
			}
			var validation *store.ErrValidation
			if !errors.As(err, &validation) {
				t.Fatalf("Parse() error = %T %v, want ErrValidation", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse() error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestToFileIncludesScopedCatalogAssets(t *testing.T) {
	t.Parallel()

	file := ToFile(
		[]fleet.Prompt{{
			ID:          "repo-coder",
			WorkspaceID: "default",
			Repo:        "eloylp/agents",
			Name:        "coder",
			Content:     "repo prompt",
		}},
		map[string]fleet.Skill{
			"workspace-skill": {
				WorkspaceID: "default",
				Name:        "team skill",
				Prompt:      "workspace skill",
			},
		},
		[]fleet.Guardrail{{
			ID:          "workspace-guardrail",
			WorkspaceID: "default",
			Name:        "workspace guardrail",
			Content:     "workspace guardrail",
		}},
	)

	if got := len(file.Assets); got != 3 {
		t.Fatalf("asset count = %d, want 3", got)
	}
	for _, asset := range file.Assets {
		if asset.WorkspaceID != "default" {
			t.Fatalf("asset %s workspace_id = %q, want default", asset.ID, asset.WorkspaceID)
		}
		if asset.ID == "repo-coder" && asset.Repo != "eloylp/agents" {
			t.Fatalf("repo-coder repo = %q, want eloylp/agents", asset.Repo)
		}
	}
}
