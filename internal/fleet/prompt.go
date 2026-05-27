package fleet

import "strings"

const GlobalCatalogScope = "global"

// Prompt is a reusable task/personality contract referenced by workspace-local
// agents. Empty WorkspaceID and Repo mean globally visible; WorkspaceID with an
// empty Repo means visible only in that workspace; WorkspaceID plus Repo means
// visible only for that repo in the workspace.
type Prompt struct {
	ID          string           `yaml:"id,omitempty" json:"id"`
	WorkspaceID string           `yaml:"workspace_id,omitempty" json:"workspace_id,omitempty"`
	Repo        string           `yaml:"repo,omitempty" json:"repo,omitempty"`
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description,omitempty" json:"description"`
	Content     string           `yaml:"content" json:"content"`
	VersionID   string           `yaml:"version_id,omitempty" json:"version_id,omitempty"`
	Version     int              `yaml:"version,omitempty" json:"version,omitempty"`
	Versions    []CatalogVersion `yaml:"versions,omitempty" json:"versions,omitempty"`
}

type CatalogVersion struct {
	ID            string `json:"id" yaml:"id,omitempty"`
	AssetID       string `json:"asset_id" yaml:"asset_id,omitempty"`
	Version       int    `json:"version" yaml:"version"`
	State         string `json:"state" yaml:"state"`
	Description   string `json:"description,omitempty" yaml:"description,omitempty"`
	Content       string `json:"content,omitempty" yaml:"content,omitempty"`
	Prompt        string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Enabled       bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Position      int    `json:"position,omitempty" yaml:"position,omitempty"`
	SourceType    string `json:"source_type,omitempty" yaml:"source_type,omitempty"`
	SourceRef     string `json:"source_ref,omitempty" yaml:"source_ref,omitempty"`
	Author        string `json:"author,omitempty" yaml:"author,omitempty"`
	Changelog     string `json:"changelog,omitempty" yaml:"changelog,omitempty"`
	BaseVersionID string `json:"base_version_id,omitempty" yaml:"base_version_id,omitempty"`
	BodyHash      string `json:"body_hash,omitempty" yaml:"body_hash,omitempty"`
	CreatedAt     string `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	PublishedAt   string `json:"published_at,omitempty" yaml:"published_at,omitempty"`
}

// CatalogVersionReference names a live fleet reference that resolves to a
// catalog version. Exact references are pinned directly to the version id;
// tracking references resolve to the asset's current published version.
type CatalogVersionReference struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	VersionID   string `json:"version_id"`
	Tracking    bool   `json:"tracking"`
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
	workspaceID = NormalizeWorkspaceID(workspaceID)
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
	workspaceID = NormalizeWorkspaceID(workspaceID)
	if !found {
		return workspaceID, "", true
	}
	return workspaceID, NormalizeRepoName(repo), true
}
