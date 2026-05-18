package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) CreateWorkspaceBinding(workspace, repo string, b fleet.Binding) (int64, fleet.Binding, error) {
	if err := validateBindingShape(b); err != nil {
		return 0, fleet.Binding{}, err
	}
	var id int64
	var created fleet.Binding
	err := s.withRawTx("create binding", func(tx *sql.Tx) error {
		var err error
		id, created, err = store.CreateWorkspaceBindingTx(tx, workspace, repo, b)
		if err != nil {
			return err
		}
		return validateFleetTx(tx)
	})
	return id, created, err
}

func (s *Service) UpdateBinding(id int64, b fleet.Binding) (fleet.Binding, error) {
	if err := validateBindingShape(b); err != nil {
		return fleet.Binding{}, err
	}
	var updated fleet.Binding
	err := s.withRawTx("update binding", func(tx *sql.Tx) error {
		var err error
		updated, err = store.UpdateBindingTx(tx, id, b)
		if err != nil {
			return err
		}
		return validateFleetTx(tx)
	})
	return updated, err
}

func (s *Service) DeleteBinding(id int64) error {
	return s.withDeleteTx("delete binding", func(tx *sql.Tx) error {
		return store.DeleteBindingTx(tx, id)
	})
}
