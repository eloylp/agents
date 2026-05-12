package fleet

import "testing"

func TestParseCatalogScopePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		scope         string
		wantWorkspace string
		wantRepo      string
		wantExplicit  bool
	}{
		{name: "empty", scope: "", wantExplicit: false},
		{name: "global", scope: "GLOBAL", wantExplicit: true},
		{name: "workspace", scope: "Default", wantWorkspace: "default", wantExplicit: true},
		{name: "repo", scope: "Default/EloyLP/Agents", wantWorkspace: "default", wantRepo: "eloylp/agents", wantExplicit: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workspace, repo, explicit := ParseCatalogScopePath(tc.scope)
			if workspace != tc.wantWorkspace || repo != tc.wantRepo || explicit != tc.wantExplicit {
				t.Fatalf("ParseCatalogScopePath(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.scope, workspace, repo, explicit, tc.wantWorkspace, tc.wantRepo, tc.wantExplicit)
			}
		})
	}
}

func TestNormalizeWorkspaceID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "empty defaults", want: "default"},
		{name: "trim and lower", id: " Team-A ", want: "team-a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeWorkspaceID(tc.id); got != tc.want {
				t.Fatalf("NormalizeWorkspaceID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestCatalogScopePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workspace string
		repo      string
		want      string
	}{
		{name: "global", want: "global"},
		{name: "workspace", workspace: "Default", want: "default"},
		{name: "repo", workspace: "Default", repo: "EloyLP/Agents", want: "default/eloylp/agents"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CatalogScopePath(tc.workspace, tc.repo); got != tc.want {
				t.Fatalf("CatalogScopePath(%q, %q) = %q, want %q", tc.workspace, tc.repo, got, tc.want)
			}
		})
	}
}
