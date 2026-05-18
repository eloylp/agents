package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) ImportConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("import config", func(tx *sql.Tx) error {
		if err := store.ImportConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}

func (s *Service) ReplaceConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("replace config", func(tx *sql.Tx) error {
		if err := store.ReplaceConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}
