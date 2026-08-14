package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) ImportConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("import config", func(tx *sql.Tx) error {
		if err := rejectDelegatedCatalogImportTx(tx, cfg); err != nil {
			return err
		}
		if err := store.ImportConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}

func (s *Service) ReplaceConfig(cfg *config.Config, budgets []store.TokenBudget) error {
	return s.withRawTx("replace config", func(tx *sql.Tx) error {
		if err := rejectDelegatedCatalogImportTx(tx, cfg); err != nil {
			return err
		}
		delegated, err := catalogDelegatedTx(tx)
		if err != nil {
			return err
		}
		replaceConfigTx := store.ReplaceConfigTx
		if delegated {
			replaceConfigTx = store.ReplaceConfigPreserveCatalogTx
		}
		if err := replaceConfigTx(tx, cfg, budgets); err != nil {
			return err
		}
		return validateFleetForCompleteConfigTx(tx)
	})
}

func rejectDelegatedCatalogImportTx(tx *sql.Tx, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Prompts) == 0 && len(cfg.Skills) == 0 && len(cfg.Guardrails) == 0 {
		return nil
	}
	return rejectCatalogDelegatedTx(tx)
}

func catalogDelegatedTx(tx *sql.Tx) (bool, error) {
	cfg, err := store.ReadCatalogDelegationConfigTx(tx)
	if err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}
