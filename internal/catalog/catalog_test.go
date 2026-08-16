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
    name: coder
    body: write code
  - id: go-api
    kind: skill
    name: go-api
    body: use handlers
  - id: security
    kind: guardrail
    name: security
    body: do not expose secrets
`,
		},
		{
			name: "same id across kinds is valid",
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
		},
		{
			name: "duplicate kind id",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    body: one
  - id: coder
    kind: prompt
    name: coder other
    body: two
`,
			wantErr: `duplicate prompt id "coder"`,
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
			name: "unsupported workflow field",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    agent: coder
    body: no
`,
			wantErr: `unsupported field "agent"`,
		},
		{
			name: "unsupported daemon-owned enabled field",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    name: coder
    enabled: true
    body: no
`,
			wantErr: `unsupported field "enabled"`,
		},
		{
			name: "unsupported daemon-owned repo field",
			body: `
version: 1
assets:
  - id: coder
    kind: prompt
    repo: eloylp/agents
    name: coder
    body: no
`,
			wantErr: `unsupported field "repo"`,
		},
		{
			name: "unsupported daemon-owned workspace field",
			body: `
version: 1
assets:
  - id: security
    kind: guardrail
    workspace_id: default
    name: security
    body: no
`,
			wantErr: `unsupported field "workspace_id"`,
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

func TestToFileOmitsDaemonOwnedCatalogMetadata(t *testing.T) {
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
		if asset.ID == "" || asset.Kind == "" || asset.Name == "" || asset.Body == "" {
			t.Fatalf("asset %s missing delegated content fields: %+v", asset.ID, asset)
		}
	}
	data, err := Marshal(file)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{"workspace_id", "repo:", "enabled:", "position:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("delegated catalog contains daemon-owned field %q:\n%s", forbidden, text)
		}
	}
}
