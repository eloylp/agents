package config

import (
	"errors"
	"testing"

	"github.com/eloylp/agents/internal/store"
)

func TestCatalogDelegationPatchRejectsSyncManagedFields(t *testing.T) {
	t.Parallel()

	sha := "abc123"
	status := "synced"
	errText := ""
	disabledAt := "2026-08-14T08:00:00Z"
	credentialStatus := "configured"

	tests := []struct {
		name  string
		patch catalogDelegationPatchJSON
	}{
		{name: "last synced commit", patch: catalogDelegationPatchJSON{LastSyncedCommit: &sha}},
		{name: "last sync status", patch: catalogDelegationPatchJSON{LastSyncStatus: &status}},
		{name: "last sync error", patch: catalogDelegationPatchJSON{LastSyncError: &errText}},
		{name: "disabled at", patch: catalogDelegationPatchJSON{DisabledAt: &disabledAt}},
		{name: "credential status", patch: catalogDelegationPatchJSON{CredentialStatus: &credentialStatus}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.patch.validatePublicPatch()
			var validation *store.ErrValidation
			if !errors.As(err, &validation) {
				t.Fatalf("validatePublicPatch() error = %T %v, want ErrValidation", err, err)
			}
		})
	}
}
