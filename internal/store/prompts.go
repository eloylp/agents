package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importPrompts(tx *sql.Tx, prompts []fleet.Prompt) error {
	for _, p := range prompts {
		p.Name = fleet.NormalizePromptName(p.Name)
		p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
		if p.WorkspaceID != "" {
			p.WorkspaceID = fleet.NormalizeWorkspaceID(p.WorkspaceID)
		}
		p.Repo = fleet.NormalizeRepoName(p.Repo)
		if p.WorkspaceID == "" && p.Repo != "" {
			return fmt.Errorf("store import: prompt %q repo scope requires workspace_id", p.Name)
		}
		if p.ID == "" {
			id, err := derivedPromptID(p)
			if err != nil {
				return fmt.Errorf("store import: prompt %q: %w", p.Name, err)
			}
			p.ID = id
		}
		if p.ID == "" || p.Name == "" {
			return fmt.Errorf("store import: prompt requires id or name")
		}
		if err := validateEntityID(p.ID); err != nil {
			return fmt.Errorf("store import: prompt %q: %w", p.Name, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO prompts (id, workspace_id, repo, name, description, content, updated_at)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				description = excluded.description,
				content = excluded.content,
				updated_at = datetime('now')`,
			p.ID, p.WorkspaceID, p.Repo, p.Name, p.Description, p.Content,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: prompt name %q is already used by another prompt in that scope", p.Name)
			}
			return fmt.Errorf("store import: upsert prompt %s: %w", p.Name, err)
		}
	}
	return nil
}

func derivedPromptID(p fleet.Prompt) (string, error) {
	return derivedCatalogID("prompt_", p.WorkspaceID, p.Repo, p.Name)
}

func queryPromptByScopeName(q querier, workspaceID, repo, name string) *sql.Row {
	return queryCatalogIDByScopeName(q, "prompts", workspaceID, repo, name)
}

func loadPrompts(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT id,COALESCE(workspace_id, ''),COALESCE(repo, ''),name,description,content FROM prompts ORDER BY COALESCE(workspace_id, ''), COALESCE(repo, ''), name")
	if err != nil {
		return fmt.Errorf("store load: query prompts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p fleet.Prompt
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content); err != nil {
			return fmt.Errorf("store load: scan prompt: %w", err)
		}
		cfg.Prompts = append(cfg.Prompts, p)
	}
	return rows.Err()
}

// ──── Prompts ───────────────────────────────────────────────────────────────────────────────────────

// UpsertPrompt inserts or replaces one prompt catalog entry. Existing rows keep
// their stable id while content, description, and scope fields are updated.
func UpsertPrompt(db *sql.DB, p fleet.Prompt) (fleet.Prompt, error) {
	tx, err := db.Begin()
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: begin: %w", p.Name, err)
	}
	defer tx.Rollback()
	p, err = UpsertPromptTx(tx, p)
	if err != nil {
		return fleet.Prompt{}, err
	}
	if err := tx.Commit(); err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: commit: %w", p.Name, err)
	}
	return p, nil
}

func UpsertPromptTx(tx *sql.Tx, p fleet.Prompt) (fleet.Prompt, error) {
	p.Name = fleet.NormalizePromptName(p.Name)
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	if p.WorkspaceID != "" {
		p.WorkspaceID = fleet.NormalizeWorkspaceID(p.WorkspaceID)
	}
	p.Repo = fleet.NormalizeRepoName(p.Repo)
	p.Description = strings.TrimSpace(p.Description)
	p.Content = strings.TrimSpace(p.Content)
	if p.Name == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt name is required"}
	}
	if p.WorkspaceID == "" && p.Repo != "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt repo scope requires workspace_id"}
	}

	var existingID string
	var err error
	if p.ID != "" {
		err = tx.QueryRow("SELECT id FROM prompts WHERE id=?", p.ID).Scan(&existingID)
	} else {
		err = queryPromptByScopeName(tx, p.WorkspaceID, p.Repo, p.Name).Scan(&existingID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: read existing: %w", p.Name, err)
	}
	if existingID != "" {
		p.ID = existingID
	} else if p.ID == "" {
		id, err := derivedPromptID(p)
		if err != nil {
			return fleet.Prompt{}, &ErrValidation{Msg: fmt.Sprintf("prompt %q: %v", p.Name, err)}
		}
		p.ID = id
	}
	if err := validateEntityID(p.ID); err != nil {
		return fleet.Prompt{}, &ErrValidation{Msg: fmt.Sprintf("prompt %q: %v", p.Name, err)}
	}
	if _, err := tx.Exec(`
		INSERT INTO prompts (id, workspace_id, repo, name, description, content, updated_at)
		VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			repo = excluded.repo,
			name = excluded.name,
			description = excluded.description,
			content = excluded.content,
			updated_at = datetime('now')`,
		p.ID, p.WorkspaceID, p.Repo, p.Name, p.Description, p.Content,
	); err != nil {
		if isUniqueConstraint(err) {
			return fleet.Prompt{}, &ErrConflict{Msg: fmt.Sprintf("prompt name %q is already used by another prompt in that scope", p.Name)}
		}
		return fleet.Prompt{}, fmt.Errorf("store: upsert prompt %s: %w", p.Name, err)
	}
	return p, nil
}

// ReadPrompt resolves a prompt by stable id first, then by legacy global display
// name. Scoped prompts may share names, so callers that need deterministic
// addressing must pass the stable id.
func ReadPrompt(db *sql.DB, ref string) (fleet.Prompt, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fleet.Prompt{}, &ErrValidation{Msg: "prompt id is required"}
	}
	var p fleet.Prompt
	row := db.QueryRow(`
		SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name, description, content
		FROM prompts
		WHERE id=?`, ref)
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content)
	if err == nil {
		return p, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s by id: %w", ref, err)
	}
	name := fleet.NormalizePromptName(ref)
	row = db.QueryRow(`
		SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name, description, content
		FROM prompts
		WHERE workspace_id IS NULL AND repo IS NULL AND name=?`, name)
	err = row.Scan(&p.ID, &p.WorkspaceID, &p.Repo, &p.Name, &p.Description, &p.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Prompt{}, &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fleet.Prompt{}, fmt.Errorf("store: read prompt %s by global name: %w", ref, err)
	}
	return p, nil
}

// DeletePrompt removes one prompt addressed by stable id, with a compatibility
// fallback for legacy global display names. A prompt referenced by any agent
// cannot be deleted because agents must always point at existing prompt content.
func DeletePrompt(db *sql.DB, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return &ErrValidation{Msg: "prompt id is required"}
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: begin: %w", ref, err)
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRow("SELECT id FROM prompts WHERE id=?", ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		name := fleet.NormalizePromptName(ref)
		err = queryPromptByScopeName(tx, "", "", name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrNotFound{Msg: fmt.Sprintf("prompt %q not found", ref)}
	}
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: lookup: %w", ref, err)
	}
	refs, err := agentsReferencingPrompt(tx, id)
	if err != nil {
		return fmt.Errorf("store: delete prompt %s: check agents: %w", ref, err)
	}
	if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("prompt %q is referenced by %d agent(s): %s", ref, len(refs), formatReferenceList(refs))}
	}
	if _, err := tx.Exec("DELETE FROM prompts WHERE id=?", id); err != nil {
		return fmt.Errorf("store: delete prompt %s: %w", ref, err)
	}
	return tx.Commit()
}

func agentsReferencingPrompt(q querier, promptID string) ([]string, error) {
	rows, err := q.Query("SELECT workspace_id, name FROM agents WHERE prompt_id=? ORDER BY workspace_id, name", promptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var workspaceID, name string
		if err := rows.Scan(&workspaceID, &name); err != nil {
			return nil, err
		}
		refs = append(refs, workspaceAgentRef(workspaceID, name))
	}
	return refs, rows.Err()
}
