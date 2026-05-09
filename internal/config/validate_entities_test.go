package config

import (
	"testing"

	"github.com/eloylp/agents/internal/fleet"
)

func TestValidateEntitiesNormalizesRepoScope(t *testing.T) {
	t.Parallel()
	agents := []fleet.Agent{{
		Name:        "reviewer",
		Backend:     "claude",
		Prompt:      "Review changes.",
		Description: "Reviews pull requests",
		ScopeType:   "repo",
		ScopeRepo:   "Owner/Repo",
	}}
	repos := []fleet.Repo{{Name: "owner/repo", Enabled: true}}
	skills := map[string]fleet.Skill{}
	backends := map[string]fleet.Backend{"claude": {Command: "claude"}}

	if err := ValidateEntities(agents, repos, skills, backends); err != nil {
		t.Fatalf("ValidateEntities: %v", err)
	}
}
