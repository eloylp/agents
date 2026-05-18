package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/store"
)

func (s *Service) CreateTokenBudget(b store.TokenBudget) (store.TokenBudget, error) {
	var saved store.TokenBudget
	err := s.withRawTx("create token budget", func(tx *sql.Tx) error {
		var err error
		saved, err = store.CreateTokenBudgetTx(tx, b)
		return err
	})
	return saved, err
}

func (s *Service) PatchTokenBudget(id int64, patch store.TokenBudgetPatch) (store.TokenBudget, error) {
	var saved store.TokenBudget
	err := s.withRawTx("patch token budget", func(tx *sql.Tx) error {
		var err error
		saved, err = store.PatchTokenBudgetTx(tx, id, patch)
		return err
	})
	return saved, err
}

func (s *Service) DeleteTokenBudget(id int64) error {
	return s.withRawTx("delete token budget", func(tx *sql.Tx) error {
		return store.DeleteTokenBudgetTx(tx, id)
	})
}
