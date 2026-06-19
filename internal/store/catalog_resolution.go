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
	return resolveVisibleCatalogRef(q, "prompts", "prompt_ref", "prompt_id", name, workspaceID, repo, catalogScopeRepo)
}

func resolveAgentPromptRef(q querier, name, promptScope, agentWorkspaceID, agentRepo string) (string, error) {
	if workspaceID, repo, explicit := fleet.ParseCatalogScopePath(promptScope); explicit {
		if !catalogScopeVisibleToAgent(workspaceID, repo, agentWorkspaceID, agentRepo) {
			return "", sql.ErrNoRows
		}
		return resolveCatalogRefInScope(q, "prompts", "prompt_ref", "prompt_id", name, workspaceID, repo, catalogScopeRepo)
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

type catalogScopeMode int

const (
	catalogScopeWorkspace catalogScopeMode = iota
	catalogScopeRepo
)

type catalogCandidate struct {
	ID          string
	Ref         string
	Name        string
	WorkspaceID string
	Repo        string
}

func resolveVisibleCatalogRef(q querier, table, label, idHint, ref, workspaceID, repo string, mode catalogScopeMode) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	if mode == catalogScopeWorkspace {
		repo = ""
	}

	candidates, err := visibleCatalogCandidates(q, table, ref, workspaceID, repo, mode)
	if err != nil {
		return "", err
	}
	candidate, err := chooseCatalogCandidate(candidates, ref, workspaceID, repo, mode)
	if err == nil {
		return candidate.ID, nil
	}
	if errors.Is(err, errAmbiguousCatalogSelector) {
		return "", ambiguousCatalogError(table, label, idHint, ref, workspaceID)
	}
	return "", err
}

func resolveCatalogRefInScope(q querier, table, label, idHint, ref, workspaceID, repo string, mode catalogScopeMode) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	if mode == catalogScopeWorkspace {
		repo = ""
	}

	candidates, err := catalogCandidatesInScope(q, table, ref, workspaceID, repo, mode)
	if err != nil {
		return "", err
	}
	candidate, err := chooseCatalogCandidate(candidates, ref, workspaceID, repo, mode)
	if err == nil {
		return candidate.ID, nil
	}
	if errors.Is(err, errAmbiguousCatalogSelector) {
		return "", ambiguousCatalogError(table, label, idHint, ref, workspaceID)
	}
	return "", err
}

func visibleCatalogCandidates(q querier, table, selector, workspaceID, repo string, mode catalogScopeMode) ([]catalogCandidate, error) {
	query := `
		SELECT id, ref, name, COALESCE(workspace_id, ''), COALESCE(repo, '')
		FROM ` + table + `
		WHERE (id = ? OR ref = ? OR name = ?)
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		  )`
	args := []any{selector, selector, selector, workspaceID, repo, workspaceID, repo}
	if mode == catalogScopeWorkspace {
		query = `
		SELECT id, ref, name, COALESCE(workspace_id, ''), ''
		FROM ` + table + `
		WHERE (id = ? OR ref = ? OR name = ?)
		  AND (workspace_id IS NULL OR workspace_id = ?)`
		args = []any{selector, selector, selector, workspaceID}
	}
	return scanCatalogCandidates(q, query, args...)
}

func catalogCandidatesInScope(q querier, table, selector, workspaceID, repo string, mode catalogScopeMode) ([]catalogCandidate, error) {
	query := `
		SELECT id, ref, name, COALESCE(workspace_id, ''), COALESCE(repo, '')
		FROM ` + table + `
		WHERE (id = ? OR ref = ? OR name = ?)
		  AND workspace_id IS ?
		  AND repo IS ?`
	var workspaceArg, repoArg any
	if workspaceID != "" {
		query = strings.Replace(query, "workspace_id IS ?", "workspace_id = ?", 1)
		workspaceArg = workspaceID
	}
	if repo != "" {
		query = strings.Replace(query, "repo IS ?", "repo = ?", 1)
		repoArg = repo
	}
	if mode == catalogScopeWorkspace {
		query = `
		SELECT id, ref, name, COALESCE(workspace_id, ''), ''
		FROM ` + table + `
		WHERE (id = ? OR ref = ? OR name = ?)
		  AND workspace_id IS ?`
		workspaceArg = nil
		if workspaceID != "" {
			query = strings.Replace(query, "workspace_id IS ?", "workspace_id = ?", 1)
			workspaceArg = workspaceID
		}
		return scanCatalogCandidates(q, query, selector, selector, selector, workspaceArg)
	}
	return scanCatalogCandidates(q, query, selector, selector, selector, workspaceArg, repoArg)
}

func scanCatalogCandidates(q querier, query string, args ...any) ([]catalogCandidate, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []catalogCandidate
	for rows.Next() {
		var c catalogCandidate
		if err := rows.Scan(&c.ID, &c.Ref, &c.Name, &c.WorkspaceID, &c.Repo); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

var errAmbiguousCatalogSelector = errors.New("ambiguous catalog selector")

func chooseCatalogCandidate(candidates []catalogCandidate, selector, workspaceID, repo string, mode catalogScopeMode) (catalogCandidate, error) {
	for _, c := range candidates {
		if c.ID == selector || c.Ref == selector {
			return c, nil
		}
	}

	var best catalogCandidate
	bestSpecificity := -1
	ambiguous := false
	for _, c := range candidates {
		if c.Name != selector {
			continue
		}
		specificity := catalogSpecificity(c, mode)
		if specificity > bestSpecificity {
			best = c
			bestSpecificity = specificity
			ambiguous = false
			continue
		}
		if specificity == bestSpecificity && c.ID != best.ID {
			ambiguous = true
		}
	}
	if bestSpecificity == -1 {
		return catalogCandidate{}, sql.ErrNoRows
	}
	if ambiguous {
		return catalogCandidate{}, errAmbiguousCatalogSelector
	}
	return best, nil
}

func catalogSpecificity(c catalogCandidate, mode catalogScopeMode) int {
	if c.WorkspaceID == "" {
		return 0
	}
	if mode == catalogScopeRepo && c.Repo != "" {
		return 2
	}
	return 1
}

func ambiguousCatalogError(table, label, idHint, selector, workspaceID string) error {
	if label == "" {
		label = strings.TrimSuffix(table, "s")
	}
	if idHint == "" {
		idHint = strings.TrimSuffix(table, "s") + " id"
	}
	return fmt.Errorf("ambiguous %s %q in workspace %q; use %s", label, selector, workspaceID, idHint)
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
