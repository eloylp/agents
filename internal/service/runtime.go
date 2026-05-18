package service

import (
	"database/sql"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

func (s *Service) WriteRuntimeSettings(settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	var saved fleet.RuntimeSettings
	err := s.withRawTx("write runtime settings", func(tx *sql.Tx) error {
		var err error
		saved, err = store.WriteRuntimeSettingsTx(tx, settings)
		return err
	})
	return saved, err
}

func (s *Service) PatchRuntimeSettings(patch store.RuntimeSettingsPatch) (fleet.RuntimeSettings, error) {
	var saved fleet.RuntimeSettings
	err := s.withRawTx("patch runtime settings", func(tx *sql.Tx) error {
		var err error
		saved, err = store.PatchRuntimeSettingsTx(tx, patch)
		return err
	})
	return saved, err
}
