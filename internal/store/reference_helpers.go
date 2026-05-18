package store

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

func workspaceAgentRef(workspaceID, name string) string {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	if workspaceID == "" {
		workspaceID = fleet.DefaultWorkspaceID
	}
	return workspaceID + "/" + name
}

func formatReferenceList(refs []string) string {
	refs = slices.Compact(slices.Sorted(slices.Values(refs)))
	if len(refs) <= 8 {
		return strings.Join(refs, ", ")
	}
	return strings.Join(refs[:8], ", ") + fmt.Sprintf(", and %d more", len(refs)-8)
}
