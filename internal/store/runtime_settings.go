package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const runtimeConfigKey = "runtime"

func ReadRuntimeSettings(db querier) (fleet.RuntimeSettings, error) {
	var raw string
	err := db.QueryRow("SELECT value FROM config WHERE key = ?", runtimeConfigKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		settings := fleet.RuntimeSettings{}
		fleet.NormalizeRuntimeSettings(&settings)
		return settings, nil
	}
	if err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: read runtime settings: %w", err)
	}
	var settings fleet.RuntimeSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: parse runtime settings: %w", err)
	}
	fleet.NormalizeRuntimeSettings(&settings)
	return settings, nil
}

func WriteRuntimeSettings(db *sql.DB, settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	fleet.NormalizeRuntimeSettings(&settings)
	if err := validateRuntimeSettings(settings); err != nil {
		return fleet.RuntimeSettings{}, err
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: marshal runtime settings: %w", err)
	}
	if _, err := db.Exec(`
		INSERT INTO config (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		runtimeConfigKey, string(body),
	); err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: write runtime settings: %w", err)
	}
	return settings, nil
}

func SetWorkspaceRunnerImage(db *sql.DB, workspaceID, image string) (fleet.Workspace, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	image = strings.TrimSpace(image)
	if image != "" && strings.ContainsAny(image, "\r\n\t ") {
		return fleet.Workspace{}, &ErrValidation{Msg: "workspace runner image must not contain whitespace"}
	}
	res, err := db.Exec("UPDATE workspaces SET runner_image = ?, updated_at = datetime('now') WHERE id = ?", image, workspaceID)
	if err != nil {
		return fleet.Workspace{}, fmt.Errorf("store: update workspace %s runner image: %w", workspaceID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fleet.Workspace{}, fmt.Errorf("store: update workspace %s runner image rows: %w", workspaceID, err)
	} else if n == 0 {
		return fleet.Workspace{}, &ErrNotFound{Msg: fmt.Sprintf("workspace %q not found", workspaceID)}
	}
	return ReadWorkspace(db, workspaceID)
}

func validateRuntimeSettings(settings fleet.RuntimeSettings) error {
	if strings.ContainsAny(settings.RunnerImage, "\r\n\t ") {
		return &ErrValidation{Msg: "runner_image must not contain whitespace"}
	}
	if settings.Constraints.PidsLimit < 0 {
		return &ErrValidation{Msg: "pids_limit must be non-negative"}
	}
	if settings.Constraints.TimeoutSeconds < 0 {
		return &ErrValidation{Msg: "timeout_seconds must be non-negative"}
	}
	return nil
}
