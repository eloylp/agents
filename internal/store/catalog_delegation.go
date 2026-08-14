package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const CatalogDelegatedMessage = "intelligence catalog is delegated to GitHub; edit catalog.yml in the configured repository instead"

// ErrCatalogDelegated is returned when a direct catalog mutation attempts to
// bypass the configured GitHub source of truth.
type ErrCatalogDelegated struct{ Msg string }

func (e *ErrCatalogDelegated) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return CatalogDelegatedMessage
}

func ReadCatalogDelegationConfig(db *sql.DB) (fleet.CatalogDelegationConfig, error) {
	return readCatalogDelegationConfig(db)
}

func ReadCatalogDelegationConfigTx(tx *sql.Tx) (fleet.CatalogDelegationConfig, error) {
	return readCatalogDelegationConfig(tx)
}

func ReadCatalogDelegationCredentialTx(tx *sql.Tx) (string, error) {
	var secret string
	if err := tx.QueryRow("SELECT credential_secret FROM catalog_delegation WHERE id=1").Scan(&secret); err != nil {
		return "", fmt.Errorf("store: read catalog delegation credential: %w", err)
	}
	return secret, nil
}

func readCatalogDelegationConfig(q querier) (fleet.CatalogDelegationConfig, error) {
	var cfg fleet.CatalogDelegationConfig
	var enabled int
	err := q.QueryRow(`
		SELECT enabled, repo, branch, catalog_path, last_synced_commit,
		       last_successful_sync_at, last_sync_status, last_sync_error,
		       disabled_at, credential_status, created_at, updated_at
		FROM catalog_delegation WHERE id=1`).Scan(
		&enabled, &cfg.Repo, &cfg.Branch, &cfg.CatalogPath, &cfg.LastSyncedCommit,
		&cfg.LastSuccessfulSyncAt, &cfg.LastSyncStatus, &cfg.LastSyncError,
		&cfg.DisabledAt, &cfg.CredentialStatus, &cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return fleet.CatalogDelegationConfig{}, fmt.Errorf("store: read catalog delegation: %w", err)
	}
	cfg.Enabled = enabled != 0
	return cfg, nil
}

type CatalogDelegationPatch struct {
	Enabled              *bool
	Repo                 *string
	Branch               *string
	CatalogPath          *string
	LastSyncedCommit     *string
	LastSuccessfulSyncAt *string
	LastSyncStatus       *string
	LastSyncError        *string
	DisabledAt           *string
	CredentialSecret     *string
	CredentialStatus     *string
}

func PatchCatalogDelegationConfig(db *sql.DB, patch CatalogDelegationPatch) (fleet.CatalogDelegationConfig, error) {
	tx, err := db.Begin()
	if err != nil {
		return fleet.CatalogDelegationConfig{}, fmt.Errorf("store: patch catalog delegation: begin: %w", err)
	}
	defer tx.Rollback()
	cfg, err := PatchCatalogDelegationConfigTx(tx, patch)
	if err != nil {
		return fleet.CatalogDelegationConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.CatalogDelegationConfig{}, fmt.Errorf("store: patch catalog delegation: commit: %w", err)
	}
	return cfg, nil
}

func PatchCatalogDelegationConfigTx(tx *sql.Tx, patch CatalogDelegationPatch) (fleet.CatalogDelegationConfig, error) {
	cfg, err := ReadCatalogDelegationConfigTx(tx)
	if err != nil {
		return fleet.CatalogDelegationConfig{}, err
	}
	enabled := cfg.Enabled
	repo := cfg.Repo
	branch := cfg.Branch
	catalogPath := cfg.CatalogPath
	lastSyncedCommit := cfg.LastSyncedCommit
	lastSuccessfulSyncAt := cfg.LastSuccessfulSyncAt
	lastSyncStatus := cfg.LastSyncStatus
	lastSyncError := cfg.LastSyncError
	disabledAt := cfg.DisabledAt
	credentialStatus := cfg.CredentialStatus
	credentialSecret := ""

	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	if patch.Repo != nil {
		repo = strings.TrimSpace(*patch.Repo)
	}
	if patch.Branch != nil {
		branch = strings.TrimSpace(*patch.Branch)
	}
	if patch.CatalogPath != nil {
		catalogPath = strings.TrimSpace(*patch.CatalogPath)
	}
	if patch.LastSyncedCommit != nil {
		lastSyncedCommit = strings.TrimSpace(*patch.LastSyncedCommit)
	}
	if patch.LastSuccessfulSyncAt != nil {
		lastSuccessfulSyncAt = strings.TrimSpace(*patch.LastSuccessfulSyncAt)
	}
	if patch.LastSyncStatus != nil {
		lastSyncStatus = strings.TrimSpace(*patch.LastSyncStatus)
	}
	if patch.LastSyncError != nil {
		lastSyncError = strings.TrimSpace(*patch.LastSyncError)
	}
	if patch.DisabledAt != nil {
		disabledAt = strings.TrimSpace(*patch.DisabledAt)
	}
	if patch.CredentialStatus != nil {
		credentialStatus = strings.TrimSpace(*patch.CredentialStatus)
	}
	if patch.CredentialSecret != nil {
		credentialSecret = strings.TrimSpace(*patch.CredentialSecret)
		if credentialSecret != "" && credentialStatus == "" {
			credentialStatus = "configured"
		}
	}
	if branch == "" {
		branch = "main"
	}
	if catalogPath == "" {
		catalogPath = "catalog.yml"
	}
	if lastSyncStatus == "" {
		if enabled {
			lastSyncStatus = "pending"
		} else {
			lastSyncStatus = "disabled"
		}
	}
	if credentialStatus == "" {
		credentialStatus = "unset"
	}
	if enabled && repo == "" {
		return fleet.CatalogDelegationConfig{}, &ErrValidation{Msg: "catalog delegation repo is required when enabled"}
	}
	if enabled && lastSyncedCommit == "" {
		return fleet.CatalogDelegationConfig{}, &ErrValidation{Msg: "catalog delegation requires a synced commit before it can be enabled"}
	}
	if _, err := tx.Exec(`
		UPDATE catalog_delegation
		SET enabled=?, repo=?, branch=?, catalog_path=?, last_synced_commit=?,
		    last_successful_sync_at=?, last_sync_status=?, last_sync_error=?,
		    disabled_at=?, credential_secret=CASE WHEN ? = '' THEN credential_secret ELSE ? END,
		    credential_status=?, updated_at=datetime('now')
		WHERE id=1`,
		boolToInt(enabled), repo, branch, catalogPath, lastSyncedCommit,
		lastSuccessfulSyncAt, lastSyncStatus, lastSyncError, disabledAt,
		credentialSecret, credentialSecret, credentialStatus,
	); err != nil {
		return fleet.CatalogDelegationConfig{}, fmt.Errorf("store: patch catalog delegation: %w", err)
	}
	return ReadCatalogDelegationConfigTx(tx)
}
