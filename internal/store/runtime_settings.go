package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const runtimeConfigKey = "runtime"

type RuntimeSettingsPatch struct {
	RunnerImage *string                 `json:"runner_image,omitempty"`
	Constraints RuntimeConstraintsPatch `json:"constraints,omitempty"`
}

type RuntimeConstraintsPatch struct {
	CPUs           *string `json:"cpus,omitempty"`
	Memory         *string `json:"memory,omitempty"`
	PidsLimit      *int64  `json:"pids_limit,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	NetworkMode    *string `json:"network_mode,omitempty"`
}

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

func PatchRuntimeSettings(db *sql.DB, patch RuntimeSettingsPatch) (fleet.RuntimeSettings, error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: begin patch runtime settings: %w", err)
	}
	defer tx.Rollback()

	current, err := ReadRuntimeSettings(tx)
	if err != nil {
		return fleet.RuntimeSettings{}, err
	}
	if patch.RunnerImage != nil {
		current.RunnerImage = *patch.RunnerImage
	}
	if patch.Constraints.CPUs != nil {
		current.Constraints.CPUs = *patch.Constraints.CPUs
	}
	if patch.Constraints.Memory != nil {
		current.Constraints.Memory = *patch.Constraints.Memory
	}
	if patch.Constraints.PidsLimit != nil {
		current.Constraints.PidsLimit = *patch.Constraints.PidsLimit
	}
	if patch.Constraints.TimeoutSeconds != nil {
		current.Constraints.TimeoutSeconds = *patch.Constraints.TimeoutSeconds
	}
	if patch.Constraints.NetworkMode != nil {
		current.Constraints.NetworkMode = *patch.Constraints.NetworkMode
	}
	updated, err := writeRuntimeSettings(tx, current)
	if err != nil {
		return fleet.RuntimeSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.RuntimeSettings{}, fmt.Errorf("store: commit patch runtime settings: %w", err)
	}
	return updated, nil
}

func WriteRuntimeSettings(db *sql.DB, settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	return writeRuntimeSettings(db, settings)
}

type runtimeSettingsWriter interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func writeRuntimeSettings(db runtimeSettingsWriter, settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
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
