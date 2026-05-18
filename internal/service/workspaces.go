package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) UpsertWorkspace(w fleet.Workspace) (fleet.Workspace, error) {
	var saved fleet.Workspace
	err := s.withRawTx("upsert workspace", func(tx *sql.Tx) error {
		var err error
		saved, err = store.UpsertWorkspaceTx(tx, w)
		return err
	})
	return saved, err
}

func (s *Service) DeleteWorkspace(workspace string) error {
	return s.withRawTx("delete workspace", func(tx *sql.Tx) error {
		return store.DeleteWorkspaceTx(tx, workspace)
	})
}

func (s *Service) SetWorkspaceRunnerImage(workspace, image string) (fleet.Workspace, error) {
	var saved fleet.Workspace
	err := s.withRawTx("set workspace runner image", func(tx *sql.Tx) error {
		if err := store.SetWorkspaceRunnerImageTx(tx, workspace, image); err != nil {
			return err
		}
		var err error
		saved, err = store.ReadWorkspace(tx, workspace)
		return err
	})
	return saved, err
}

func (s *Service) ReplaceWorkspaceGuardrails(workspace string, refs []fleet.WorkspaceGuardrailRef) ([]fleet.WorkspaceGuardrailRef, error) {
	var saved []fleet.WorkspaceGuardrailRef
	err := s.withRawTx("replace workspace guardrails", func(tx *sql.Tx) error {
		var err error
		saved, err = store.ReplaceWorkspaceGuardrailsTx(tx, workspace, refs)
		return err
	})
	return saved, err
}
