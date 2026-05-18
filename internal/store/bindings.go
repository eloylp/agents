package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

// ──── Bindings (atomic per-item CRUD) ────────────────────────────────────────────

// normalizeBinding lowercases/trims agent and event names so writes match the
// canonical form the daemon derives at boot (see normalize() in config.go).
func normalizeBinding(b *fleet.Binding) {
	b.Agent = strings.ToLower(strings.TrimSpace(b.Agent))
	b.Cron = strings.TrimSpace(b.Cron)
	for i := range b.Events {
		b.Events[i] = strings.ToLower(strings.TrimSpace(b.Events[i]))
	}
}

// CreateBinding inserts a new binding row for the given repo and returns the
// generated ID along with the canonical (normalized) Binding the store
// persisted. The binding must satisfy trigger-exclusivity, cron parseability,
// and repo/agent reference checks; the repo and agent must both exist.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and post-write fleet validation, or use
// CreateWorkspaceBindingTx inside a service-owned transaction.
//
// Validation failures surface as *ErrValidation (HTTP 400). Missing repo/agent
// references surface as *ErrNotFound (HTTP 404). The caller is responsible for
// holding the store mutex and reloading cron schedules after success.
func CreateBinding(db *sql.DB, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	return CreateWorkspaceBinding(db, fleet.DefaultWorkspaceID, repoName, b)
}

func CreateWorkspaceBinding(db *sql.DB, workspaceID, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	shape := b
	normalizeBinding(&shape)
	if err := fleet.ValidateBindingShape(shape); err != nil {
		return 0, fleet.Binding{}, &ErrValidation{Msg: err.Error()}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: begin: %w", err)
	}
	defer tx.Rollback()
	id, created, err := CreateWorkspaceBindingTx(tx, workspaceID, repoName, b)
	if err != nil {
		return 0, fleet.Binding{}, err
	}
	if err := validateFleet(tx); err != nil {
		return 0, fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("store: create binding: %v", err)}
	}
	if err := tx.Commit(); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: commit: %w", err)
	}
	return id, created, nil
}

// CreateWorkspaceBindingTx inserts a binding inside an existing transaction.
// Callers should validate the binding shape before calling this primitive.
func CreateWorkspaceBindingTx(tx *sql.Tx, workspaceID, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repoName = fleet.NormalizeRepoName(repoName)
	normalizeBinding(&b)

	// Verify the repo exists so the FK violation surfaces as a typed error.
	var repoExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)", workspaceID, repoName).Scan(&repoExists); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: lookup repo: %w", err)
	}
	if !repoExists {
		return 0, fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("repo %q not found", repoName)}
	}
	var agentExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE workspace_id=? AND name=?)", workspaceID, b.Agent).Scan(&agentExists); err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: lookup agent: %w", err)
	}
	if !agentExists {
		return 0, fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("unknown agent %q", b.Agent)}
	}

	labels, err := json.Marshal(nilSafeStrings(b.Labels))
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: marshal labels: %w", err)
	}
	events, err := json.Marshal(nilSafeStrings(b.Events))
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: marshal events: %w", err)
	}
	enabled := bindingEnabledInt(b.Enabled)
	res, err := tx.Exec(
		`INSERT INTO bindings(workspace_id,repo,agent,labels,events,cron,enabled) VALUES (?,?,?,?,?,?,?)`,
		workspaceID, repoName, b.Agent, string(labels), string(events), b.Cron, enabled,
	)
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fleet.Binding{}, fmt.Errorf("store: create binding: last insert id: %w", err)
	}
	b.ID = id
	return id, b, nil
}

// UpdateBinding replaces the row identified by id with the new values. All
// binding fields (agent, labels, events, cron, enabled) are overwritten.
// Returns *ErrNotFound when no row matches, *ErrValidation for bad shapes or
// unknown agent refs.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and post-write fleet validation, or use UpdateBindingTx
// inside a service-owned transaction.
//
// Callers hold the store mutex and reload cron afterwards.
func UpdateBinding(db *sql.DB, id int64, b fleet.Binding) (fleet.Binding, error) {
	shape := b
	normalizeBinding(&shape)
	if err := fleet.ValidateBindingShape(shape); err != nil {
		return fleet.Binding{}, &ErrValidation{Msg: err.Error()}
	}
	tx, err := db.Begin()
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: begin: %w", err)
	}
	defer tx.Rollback()
	updated, err := UpdateBindingTx(tx, id, b)
	if err != nil {
		return fleet.Binding{}, err
	}
	if err := validateFleet(tx); err != nil {
		return fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("store: update binding: %v", err)}
	}
	if err := tx.Commit(); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: commit: %w", err)
	}
	return updated, nil
}

// UpdateBindingTx replaces one binding inside an existing transaction. Callers
// should validate the binding shape before calling this primitive.
func UpdateBindingTx(tx *sql.Tx, id int64, b fleet.Binding) (fleet.Binding, error) {
	normalizeBinding(&b)

	var workspaceID string
	if err := tx.QueryRow("SELECT workspace_id FROM bindings WHERE id=?", id).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
		}
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup workspace: %w", err)
	}
	var agentExists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM agents WHERE workspace_id=? AND name=?)", workspaceID, b.Agent).Scan(&agentExists); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup agent: %w", err)
	}
	if !agentExists {
		return fleet.Binding{}, &ErrValidation{Msg: fmt.Sprintf("unknown agent %q", b.Agent)}
	}

	labels, err := json.Marshal(nilSafeStrings(b.Labels))
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: marshal labels: %w", err)
	}
	events, err := json.Marshal(nilSafeStrings(b.Events))
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: marshal events: %w", err)
	}
	enabled := bindingEnabledInt(b.Enabled)
	res, err := tx.Exec(
		`UPDATE bindings SET agent=?, labels=?, events=?, cron=?, enabled=? WHERE id=?`,
		b.Agent, string(labels), string(events), b.Cron, enabled, id,
	)
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: rows affected: %w", err)
	}
	if n == 0 {
		return fleet.Binding{}, &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
	}

	var repoName string
	if err := tx.QueryRow("SELECT repo FROM bindings WHERE id=?", id).Scan(&repoName); err != nil {
		return fleet.Binding{}, fmt.Errorf("store: update binding: lookup repo: %w", err)
	}

	b.ID = id
	return b, nil
}

// DeleteBinding removes the row with the given id. Returns *ErrNotFound if
// no row matches. Post-delete validateFleet runs to catch any cross-entity
// invariant violations.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and post-delete fleet validation, or use DeleteBindingTx
// inside a service-owned transaction.
//
// Callers hold the store mutex and reload cron afterwards.
func DeleteBinding(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete binding: begin: %w", err)
	}
	defer tx.Rollback()
	if err := DeleteBindingTx(tx, id); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrConflict{Msg: fmt.Sprintf("store: delete binding: %v", err)}
	}
	return tx.Commit()
}

func DeleteBindingTx(tx *sql.Tx, id int64) error {
	res, err := tx.Exec("DELETE FROM bindings WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("store: delete binding: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete binding: rows affected: %w", err)
	}
	if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found", id)}
	}
	return nil
}

// ReadBinding fetches a single binding by ID along with its parent repo name.
// Returns found=false when the row does not exist; errors only reflect
// unexpected I/O failures.
func ReadBinding(db *sql.DB, id int64) (repoName string, b fleet.Binding, found bool, err error) {
	_, repoName, b, found, err = ReadWorkspaceBinding(db, id)
	return repoName, b, found, err
}

func ReadWorkspaceBinding(db *sql.DB, id int64) (workspaceID, repoName string, b fleet.Binding, found bool, err error) {
	var labelsJSON, eventsJSON, cron, agent string
	var enabled int
	err = db.QueryRow(
		"SELECT workspace_id,repo,agent,labels,events,cron,enabled FROM bindings WHERE id=?", id,
	).Scan(&workspaceID, &repoName, &agent, &labelsJSON, &eventsJSON, &cron, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fleet.Binding{}, false, nil
	}
	if err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: %w", id, err)
	}
	var labels []string
	if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: parse labels: %w", id, err)
	}
	var events []string
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return "", "", fleet.Binding{}, false, fmt.Errorf("store: read binding %d: parse events: %w", id, err)
	}
	b = fleet.Binding{
		ID:     id,
		Agent:  agent,
		Labels: labels,
		Events: events,
		Cron:   cron,
	}
	if enabled == 0 {
		f := false
		b.Enabled = &f
	}
	return workspaceID, repoName, b, true, nil
}

// nilSafeStrings normalises nil to empty slices so JSON marshalling stays
// stable ([] rather than null). The webhook layer has the same helper; duped
// here so the store package does not import it.
func nilSafeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
