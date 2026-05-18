package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func loadRuntimeSettings(db querier, cfg *config.Config) error {
	settings, err := ReadRuntimeSettings(db)
	if err != nil {
		return err
	}
	cfg.Runtime = settings
	return nil
}

func importRuntimeSettings(tx *sql.Tx, settings fleet.RuntimeSettings) error {
	fleet.NormalizeRuntimeSettings(&settings)
	if err := validateRuntimeSettings(settings); err != nil {
		return err
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("store import: marshal runtime settings: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO config (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		runtimeConfigKey, string(body),
	); err != nil {
		return fmt.Errorf("store import: upsert runtime settings: %w", err)
	}
	return nil
}
