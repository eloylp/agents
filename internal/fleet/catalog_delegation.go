package fleet

// CatalogDelegationConfig describes the daemon-owned delegation state for the
// reusable intelligence catalog. Secrets are intentionally represented only as
// a redacted status string at this layer.
type CatalogDelegationConfig struct {
	Enabled              bool   `json:"enabled" yaml:"enabled"`
	Repo                 string `json:"repo,omitempty" yaml:"repo,omitempty"`
	Branch               string `json:"branch,omitempty" yaml:"branch,omitempty"`
	CatalogPath          string `json:"catalog_path,omitempty" yaml:"catalog_path,omitempty"`
	LastSyncedCommit     string `json:"last_synced_commit,omitempty" yaml:"last_synced_commit,omitempty"`
	LastSuccessfulSyncAt string `json:"last_successful_sync_at,omitempty" yaml:"last_successful_sync_at,omitempty"`
	LastSyncStatus       string `json:"last_sync_status,omitempty" yaml:"last_sync_status,omitempty"`
	LastSyncError        string `json:"last_sync_error,omitempty" yaml:"last_sync_error,omitempty"`
	DisabledAt           string `json:"disabled_at,omitempty" yaml:"disabled_at,omitempty"`
	CredentialStatus     string `json:"credential_status,omitempty" yaml:"credential_status,omitempty"`
	CreatedAt            string `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}
