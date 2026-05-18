package service

import (
	"database/sql"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) UpsertAgent(a fleet.Agent) error {
	if strings.TrimSpace(a.Name) == "" {
		return &store.ErrValidation{Msg: "name is required"}
	}
	if strings.TrimSpace(a.PromptID) != "" {
		// prompt_id is the persisted reference. prompt_ref/prompt_scope are
		// selectors accepted at the service boundary and resolved by store.
		a.PromptRef = ""
		a.PromptScope = ""
	}
	if strings.TrimSpace(a.PromptRef) == "" && strings.TrimSpace(a.PromptID) == "" {
		return &store.ErrValidation{Msg: "prompt_id or prompt_ref is required"}
	}
	return s.withTx("upsert agent", func(tx *sql.Tx) error {
		return store.UpsertAgentTx(tx, a)
	})
}

func (s *Service) DeleteWorkspaceAgent(workspace, name string) error {
	return s.withDeleteTx("delete agent", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceAgentTx(tx, workspace, name, false)
	})
}

func (s *Service) DeleteWorkspaceAgentCascade(workspace, name string) error {
	return s.withDeleteTx("delete agent cascade", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceAgentTx(tx, workspace, name, true)
	})
}
