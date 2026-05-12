package fleet

import "strings"

const GlobalCatalogScope = "global"

// Prompt is a reusable task/personality contract referenced by workspace-local
// agents. Empty WorkspaceID and Repo mean globally visible; WorkspaceID with an
// empty Repo means visible only in that workspace; WorkspaceID plus Repo means
// visible only for that repo in the workspace.
type Prompt struct {
	ID          string `yaml:"id,omitempty" json:"id"`
	WorkspaceID string `yaml:"workspace_id,omitempty" json:"workspace_id,omitempty"`
	Repo        string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description"`
	Content     string `yaml:"content" json:"content"`
}

// CatalogScopePath is the human-facing selector path for a reusable catalog
// item. Empty workspace/repo means "global"; workspace only means the item is
// visible at workspace level; workspace plus repo means the repo-scoped item.
func CatalogScopePath(workspaceID, repo string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	repo = NormalizeRepoName(repo)
	if workspaceID == "" && repo == "" {
		return GlobalCatalogScope
	}
	if workspaceID == "" {
		workspaceID = DefaultWorkspaceID
	}
	workspaceID = strings.ToLower(NormalizeWorkspaceID(workspaceID))
	if repo == "" {
		return workspaceID
	}
	return workspaceID + "/" + repo
}

// ParseCatalogScopePath parses "global", "workspace", or "workspace/owner/repo".
// The repo part intentionally keeps its owner/repo slash; parsing splits only
// at the first slash.
func ParseCatalogScopePath(scope string) (workspaceID, repo string, explicit bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", "", false
	}
	lower := strings.ToLower(scope)
	if lower == GlobalCatalogScope {
		return "", "", true
	}
	workspaceID, repo, found := strings.Cut(lower, "/")
	workspaceID = strings.ToLower(NormalizeWorkspaceID(workspaceID))
	if !found {
		return workspaceID, "", true
	}
	return workspaceID, NormalizeRepoName(repo), true
}
