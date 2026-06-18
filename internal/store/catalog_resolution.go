package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

func queryCatalogIDByScopeName(q querier, table, workspaceID, repo, name string) *sql.Row {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	}
	repo = fleet.NormalizeRepoName(repo)
	if workspaceID == "" {
		return q.QueryRow("SELECT id FROM "+table+" WHERE workspace_id IS NULL AND repo IS NULL AND name=?", name)
	}
	if repo == "" {
		return q.QueryRow("SELECT id FROM "+table+" WHERE workspace_id=? AND repo IS NULL AND name=?", workspaceID, name)
	}
	return q.QueryRow("SELECT id FROM "+table+" WHERE workspace_id=? AND repo=? AND name=?", workspaceID, repo, name)
}

func queryCatalogRefByScopeName(q querier, table, workspaceID, repo, name string) *sql.Row {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	}
	repo = fleet.NormalizeRepoName(repo)
	if workspaceID == "" {
		return q.QueryRow("SELECT id, ref FROM "+table+" WHERE workspace_id IS NULL AND repo IS NULL AND name=?", name)
	}
	if repo == "" {
		return q.QueryRow("SELECT id, ref FROM "+table+" WHERE workspace_id=? AND repo IS NULL AND name=?", workspaceID, name)
	}
	return q.QueryRow("SELECT id, ref FROM "+table+" WHERE workspace_id=? AND repo=? AND name=?", workspaceID, repo, name)
}

func resolveVisiblePromptByName(q querier, name, workspaceID, repo string) (string, error) {
	return resolveVisibleCatalogName(q, "prompts", "prompt_ref", "prompt_id", name, workspaceID, repo)
}

func resolveAgentPromptRef(q querier, name, promptScope, agentWorkspaceID, agentRepo string) (string, error) {
	if workspaceID, repo, explicit := fleet.ParseCatalogScopePath(promptScope); explicit {
		if !catalogScopeVisibleToAgent(workspaceID, repo, agentWorkspaceID, agentRepo) {
			return "", sql.ErrNoRows
		}
		var id string
		if err := queryPromptByScopeName(q, workspaceID, repo, name).Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	}
	return resolveVisiblePromptByName(q, name, agentWorkspaceID, agentRepo)
}

func catalogScopeVisibleToAgent(scopeWorkspaceID, scopeRepo, agentWorkspaceID, agentRepo string) bool {
	scopeWorkspaceID = strings.TrimSpace(scopeWorkspaceID)
	if scopeWorkspaceID != "" {
		scopeWorkspaceID = fleet.NormalizeWorkspaceID(scopeWorkspaceID)
	}
	scopeRepo = fleet.NormalizeRepoName(scopeRepo)
	agentWorkspaceID = fleet.NormalizeWorkspaceID(agentWorkspaceID)
	agentRepo = fleet.NormalizeRepoName(agentRepo)
	if scopeWorkspaceID == "" && scopeRepo == "" {
		return true
	}
	if scopeWorkspaceID != agentWorkspaceID {
		return false
	}
	return scopeRepo == "" || (agentRepo != "" && scopeRepo == agentRepo)
}

func ensureCatalogScope(tx *sql.Tx, kind, workspaceID, repo string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	}
	repo = fleet.NormalizeRepoName(repo)
	if workspaceID == "" {
		return nil
	}
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO workspaces (id, name, description, runner_image, updated_at)
		VALUES (?, ?, '', '', datetime('now'))`,
		workspaceID, workspaceNameFromID(workspaceID),
	)
	if err != nil {
		return fmt.Errorf("store: ensure %s workspace %s: %w", kind, workspaceID, err)
	}
	if inserted, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: check %s workspace %s insert result: %w", kind, workspaceID, err)
	} else if inserted > 0 {
		if err := seedWorkspaceGuardrails(tx, workspaceID); err != nil {
			return err
		}
	}
	if repo == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM repos WHERE workspace_id=? AND name=?)", workspaceID, repo).Scan(&exists); err != nil {
		return fmt.Errorf("store: check %s repo scope %s/%s: %w", kind, workspaceID, repo, err)
	}
	if !exists {
		return &ErrValidation{Msg: fmt.Sprintf("%s repo scope %q requires an existing repo in workspace %q", kind, repo, workspaceID)}
	}
	return nil
}

func resolveVisibleCatalogRef(q querier, table, ref, workspaceID, repo string) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)

	exactID, exactName, err := resolveVisibleCatalogExactRef(q, table, ref, workspaceID, repo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := q.Query(`
		SELECT id,
			CASE
				WHEN workspace_id IS NULL THEN 0
				WHEN repo IS NULL THEN 1
				ELSE 2
			END AS specificity
		FROM `+table+`
		WHERE name = ?
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		  )
		ORDER BY specificity DESC, id`,
		ref, workspaceID, repo, workspaceID, repo,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var bestID string
	bestSpecificity := -1
	ambiguous := false
	for rows.Next() {
		var id string
		var specificity int
		if err := rows.Scan(&id, &specificity); err != nil {
			return "", err
		}
		if bestSpecificity == -1 {
			bestID = id
			bestSpecificity = specificity
			continue
		}
		if specificity == bestSpecificity && id != bestID {
			ambiguous = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if bestSpecificity == -1 {
		if exactID != "" {
			return exactID, nil
		}
		return "", sql.ErrNoRows
	}
	if exactID != "" && exactName != ref {
		return exactID, nil
	}
	if ambiguous {
		label := strings.TrimSuffix(table, "s")
		return "", fmt.Errorf("ambiguous %s %q in workspace %q; use %s id", label, ref, workspaceID, label)
	}
	return bestID, nil
}

func resolveVisibleCatalogExactRef(q querier, table, ref, workspaceID, repo string) (string, string, error) {
	var id, name string
	err := q.QueryRow(`
		SELECT id, name
		FROM `+table+`
		WHERE (id = ? OR ref = ?)
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		  )
		ORDER BY
			CASE
				WHEN workspace_id IS NULL THEN 0
				WHEN repo IS NULL THEN 1
				ELSE 2
			END DESC,
			id
		LIMIT 1`,
		ref, ref, workspaceID, repo, workspaceID, repo,
	).Scan(&id, &name)
	return id, name, err
}

func resolveVisibleCatalogID(q querier, table, id, workspaceID, repo string) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	rows, err := q.Query(`
		SELECT id
		FROM `+table+`
		WHERE (id = ? OR ref = ?)
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		)
		LIMIT 1`,
		id, id, workspaceID, repo, workspaceID, repo,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		return id, rows.Err()
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", sql.ErrNoRows
}

func resolveCatalogID(q querier, table, ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	var id, publicRef string
	err := q.QueryRow("SELECT id, ref FROM "+table+" WHERE id=? OR ref=?", ref, ref).Scan(&id, &publicRef)
	return id, publicRef, err
}

func newCatalogInternalID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate catalog id: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func resolveVisibleCatalogName(q querier, table, label, idHint, name, workspaceID, repo string) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	rows, err := q.Query(`
		SELECT id
		FROM `+table+`
		WHERE name = ?
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		  )
		ORDER BY
			CASE
				WHEN workspace_id IS NULL THEN 0
				WHEN repo IS NULL THEN 1
				ELSE 2
			END`,
		name, workspaceID, repo, workspaceID, repo,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", sql.ErrNoRows
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("ambiguous %s %q in workspace %q; use %s", label, name, workspaceID, idHint)
	}
	return ids[0], nil
}

func derivedCatalogID(prefix, workspaceID, repo, name string) (string, error) {
	parts := []string{}
	if workspaceID != "" {
		parts = append(parts, workspaceID)
	}
	if repo != "" {
		parts = append(parts, strings.ReplaceAll(repo, "/", "_"))
	}
	parts = append(parts, name)
	scope := strings.Join(parts, "_")
	return derivedEntityID(prefix, scope)
}

func derivedEntityID(prefix, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	id := prefix + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if err := validateEntityID(id); err != nil {
		return "", fmt.Errorf("derived id %q is not URL-safe; set an explicit id using lowercase letters, digits, hyphens, and underscores", id)
	}
	return id, nil
}

func validateEntityID(id string) error {
	if id == "" {
		return nil
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' {
			continue
		}
		if r == '_' {
			continue
		}
		return fmt.Errorf("id %q must contain only lowercase letters, digits, hyphens, and underscores", id)
	}
	return nil
}

func isUniqueConstraint(err error) bool {
	// modernc.org/sqlite v1.49.1 includes both fragments for UNIQUE failures;
	// tests cover the friendly errors that depend on this compatibility shim.
	return strings.Contains(err.Error(), "constraint failed") && strings.Contains(err.Error(), "UNIQUE")
}
