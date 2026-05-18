// Package service owns mutable fleet/config use cases.
//
// REST and MCP handlers should decode wire shapes, call this package, then map
// typed errors back to their transport response. The service keeps mutation
// orchestration out of handlers while the store package remains the SQLite
// persistence boundary.
package service

import (
	"database/sql"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// Service coordinates mutable fleet/config operations against the store.
type Service struct {
	store *store.Store
}

// New constructs a service layer backed by st.
func New(st *store.Store) *Service {
	return &Service{store: st}
}

func (s *Service) withTx(op string, fn func(*sql.Tx) error) error {
	return s.withRawTx(op, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return validateFleetTx(tx)
	})
}

func (s *Service) withDeleteTx(op string, fn func(*sql.Tx) error) error {
	return s.withRawTx(op, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := validateFleetTx(tx); err != nil {
			return &store.ErrConflict{Msg: err.Error()}
		}
		if err := validateFleetMinimumsTx(tx); err != nil {
			return &store.ErrConflict{Msg: err.Error()}
		}
		return nil
	})
}

func (s *Service) withRawTx(op string, fn func(*sql.Tx) error) error {
	tx, err := s.store.DB().Begin()
	if err != nil {
		return fmt.Errorf("service: %s: begin: %w", op, err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("service: %s: commit: %w", op, err)
	}
	return nil
}

func validateFleetMinimumsTx(tx *sql.Tx) error {
	cfg, err := store.LoadFleetConfigTx(tx)
	if err != nil {
		return err
	}
	if len(cfg.Agents) == 0 {
		return &store.ErrValidation{Msg: "service: validate fleet: config: at least one agent is required"}
	}
	backends := cfg.Daemon.AIBackends
	if len(backends) == 0 {
		return &store.ErrValidation{Msg: "service: validate fleet: config: at least one backend entry is required"}
	}
	return nil
}

func validateFleetForCompleteConfigTx(tx *sql.Tx) error {
	if err := validateFleetTx(tx); err != nil {
		return err
	}
	return validateFleetMinimumsTx(tx)
}

func validateFleetTx(tx *sql.Tx) error {
	cfg, err := store.LoadFleetConfigTx(tx)
	if err != nil {
		return err
	}
	backends := cfg.Daemon.AIBackends
	if backends == nil {
		backends = map[string]fleet.Backend{}
	}
	skills := cfg.Skills
	if skills == nil {
		skills = map[string]fleet.Skill{}
	}
	if err := config.ValidateEntities(cfg.Agents, cfg.Repos, skills, backends); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	if err := config.ValidateAgentCatalogVisibility(cfg.Agents, skills); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	if err := fleet.ValidateRepoCronExpressions(cfg.Repos); err != nil {
		return &store.ErrValidation{Msg: fmt.Sprintf("service: validate fleet: %v", err)}
	}
	return nil
}

func validateBindingShape(b fleet.Binding) error {
	if err := fleet.ValidateBindingShape(b); err != nil {
		return &store.ErrValidation{Msg: err.Error()}
	}
	return nil
}
