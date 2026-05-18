package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importBackends(tx *sql.Tx, backends map[string]fleet.Backend) error {
	for name, b := range backends {
		models, err := json.Marshal(b.Models)
		if err != nil {
			return fmt.Errorf("store import: marshal backend %s models: %w", name, err)
		}
		healthy := boolToInt(b.Healthy)
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO backends
			  (name,command,version,models,healthy,health_detail,local_model_url,timeout_seconds,max_prompt_chars)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			name, b.Command,
			b.Version, string(models), healthy, b.HealthDetail, b.LocalModelURL,
			b.TimeoutSeconds, b.MaxPromptChars,
		); err != nil {
			return fmt.Errorf("store import: upsert backend %s: %w", name, err)
		}
	}
	return nil
}

func loadBackends(db querier, cfg *config.Config) error {
	// redaction_salt_env was removed when prompts started being stored
	// directly on traces. The column is left in the table (NULL on every
	// new row) but no longer mapped to a struct field.
	rows, err := db.Query("SELECT name,command,version,models,healthy,health_detail,local_model_url,timeout_seconds,max_prompt_chars FROM backends")
	if err != nil {
		return fmt.Errorf("store load: query backends: %w", err)
	}
	defer rows.Close()

	backends := make(map[string]fleet.Backend)
	for rows.Next() {
		var name, command, version, modelsJSON, healthDetail, localModelURL string
		var timeout, maxChars, healthy int
		if err := rows.Scan(&name, &command, &version, &modelsJSON, &healthy, &healthDetail, &localModelURL, &timeout, &maxChars); err != nil {
			return fmt.Errorf("store load: scan backend: %w", err)
		}
		var models []string
		if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
			return fmt.Errorf("store load: parse backend %s models: %w", name, err)
		}
		backends[name] = fleet.Backend{
			Command:        command,
			Version:        version,
			Models:         models,
			Healthy:        intToBool(healthy),
			HealthDetail:   healthDetail,
			LocalModelURL:  localModelURL,
			TimeoutSeconds: timeout,
			MaxPromptChars: maxChars,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate backends: %w", err)
	}
	cfg.Daemon.AIBackends = backends
	cfg.Backends = backends
	return nil
}

// ──── Backends ───────────────────────────────────────────────────────────────────────────────────────

// ReadBackends returns all AI backend configurations from the database.
func ReadBackends(db *sql.DB) (map[string]fleet.Backend, error) {
	var cfg config.Config
	if err := loadBackends(db, &cfg); err != nil {
		return nil, err
	}
	if cfg.Daemon.AIBackends == nil {
		return map[string]fleet.Backend{}, nil
	}
	return cfg.Daemon.AIBackends, nil
}

// UpsertBackend inserts or replaces a single AI backend configuration.
// Before writing, the backend is fully normalized to match what startup
// produces: the name is lowercased and trimmed, Command is trimmed, blank env
// keys are removed, and zero-value numeric fields are filled with startup
// defaults (timeout_seconds 0 → 600, max_prompt_chars 0 → 12000). This
// ensures the stored values are already in canonical form so that live
// behavior never diverges from a post-restart load.
func UpsertBackend(db *sql.DB, name string, b fleet.Backend) error {
	name = fleet.NormalizeBackendName(name)
	fleet.NormalizeBackend(&b)
	fleet.ApplyBackendDefaults(&b)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := importBackends(tx, map[string]fleet.Backend{name: b}); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert backend %s: %v", name, err)}
	}
	return tx.Commit()
}

// DeleteBackend removes the backend with the given name. Returns an error if
// any agent still references the backend, or if it is the last backend.
func DeleteBackend(db *sql.DB, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete backend %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM backends WHERE name=?", name)
	if err != nil {
		return fmt.Errorf("store: delete backend %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err := requireAtLeastOne(tx, "SELECT COUNT(*) FROM backends", "backends", "config: at least one backend entry is required"); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete backend %s: %v", name, err)}
		}
		if err := validateFleet(tx); err != nil {
			return &ErrConflict{Msg: fmt.Sprintf("store: delete backend %s: %v", name, err)}
		}
	}
	return tx.Commit()
}
