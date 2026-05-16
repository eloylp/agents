// Package store manages the SQLite-backed runtime configuration store.
//
// Phase 1 covers the read/write path for all entities that previously lived in
// config.yaml: daemon settings, AI backends, skills, agents, repos, and
// repo-agent bindings. The config.Config type is used as the exchange format
// so that all downstream code (scheduler, engine, webhook server) requires no
// changes.
//
// Usage:
//
//	db, err := store.Open("/var/lib/agents/agents.db")
//	// First-time import from YAML:
//	cfg, _ := config.Load("config.yaml")
//	store.Import(db, cfg)
//	// Subsequent starts, read from DB:
//	cfg, err = store.Load(db)
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register the sqlite3 driver

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// querier is a minimal interface satisfied by both *sql.DB and *sql.Tx,
// allowing load helpers to run inside or outside a transaction without
// duplicating query logic.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens (or creates) a SQLite database at path and runs all pending
// schema migrations. It returns a ready-to-use *sql.DB.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// Use WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: enable foreign keys: %w", err)
	}
	// Retry for up to 5 s when another goroutine holds a write lock instead of
	// returning SQLITE_BUSY immediately. The observe store records spans,
	// events, and dispatches on concurrent goroutines, so without a timeout
	// those writes race and one of them can fail with "database is locked".
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: set busy timeout: %w", err)
	}
	// Pin to a single connection so PRAGMAs set above (busy_timeout, WAL mode,
	// foreign_keys) apply to every subsequent operation. Without this,
	// database/sql may open additional connections that bypass those settings,
	// causing spurious SQLITE_BUSY under concurrent goroutine writes.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Import writes cfg into the database, upserting every entity. Existing rows
// are replaced (INSERT OR REPLACE). Prompts are stored inline, agents and
// skills must carry their full prompt text in cfg.Prompt before calling
// Import.
//
// Secrets (WebhookSecret) are NOT written, only the env-var name
// (WebhookSecretEnv) is stored. The secret is re-resolved from the
// environment at Load time.
func Import(db *sql.DB, cfg *config.Config) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store import: begin: %w", err)
	}
	defer tx.Rollback()

	backends := cfg.Daemon.AIBackends
	if len(backends) == 0 {
		backends = cfg.Backends
	}
	if err := importBackends(tx, backends); err != nil {
		return err
	}
	if err := importRuntimeSettings(tx, cfg.Runtime); err != nil {
		return err
	}
	if err := importSkills(tx, cfg.Skills); err != nil {
		return err
	}
	if err := importGuardrails(tx, cfg.Guardrails); err != nil {
		return err
	}
	if err := importWorkspaces(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importWorkspaceGuardrails(tx, cfg.Workspaces); err != nil {
		return err
	}
	if err := importPrompts(tx, cfg.Prompts); err != nil {
		return err
	}
	if err := importReferencedWorkspaces(tx, cfg.Agents, cfg.Repos); err != nil {
		return err
	}
	if err := importAgents(tx, cfg.Agents, false); err != nil {
		return err
	}
	if err := importRepos(tx, cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

// importGuardrails upserts each guardrail using the same ON CONFLICT shape
// as store.UpsertGuardrail: existing rows have their description, content,
// enabled, and position fields updated; is_builtin and default_content are
// preserved (they are migration-controlled and intentionally not editable
// from YAML). Operator-added rows are inserted with default_content NULL
// and is_builtin = 0.
func importGuardrails(tx *sql.Tx, guardrails []fleet.Guardrail) error {
	for _, g := range guardrails {
		fleet.NormalizeGuardrail(&g)
		if g.WorkspaceID == "" && g.Repo != "" {
			return fmt.Errorf("store import: guardrail %q repo scope requires workspace_id", g.Name)
		}
		if g.ID == "" {
			var existingID string
			err := queryCatalogIDByScopeName(tx, "guardrails", g.WorkspaceID, g.Repo, g.Name).Scan(&existingID)
			if err == nil {
				g.ID = existingID
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("store import: read guardrail %s: %w", g.Name, err)
			}
		}
		if g.ID == "" {
			id, err := derivedCatalogID("guardrail_", g.WorkspaceID, g.Repo, g.Name)
			if err != nil {
				return fmt.Errorf("store import: guardrail %q: %w", g.Name, err)
			}
			g.ID = id
		}
		if g.Name == "" || g.Content == "" {
			return fmt.Errorf("store import: guardrail requires name and content (got name=%q)", g.Name)
		}
		if err := validateEntityID(g.ID); err != nil {
			return fmt.Errorf("store import: guardrail %q: %w", g.Name, err)
		}
		if isReservedGuardrailName(g.Name) {
			return fmt.Errorf("store import: guardrail name %q is reserved for runtime-generated policy", g.Name)
		}
		enabled := boolToInt(g.Enabled)
		if _, err := tx.Exec(`
			INSERT INTO guardrails (id, workspace_id, repo, name, description, content, enabled, position, updated_at)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				description = excluded.description,
				content     = excluded.content,
				enabled     = excluded.enabled,
				position    = excluded.position,
				updated_at  = datetime('now')`,
			g.ID, g.WorkspaceID, g.Repo, g.Name, g.Description, g.Content, enabled, g.Position,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: guardrail name %q is already used by another guardrail in that scope", g.Name)
			}
			return fmt.Errorf("store import: upsert guardrail %s: %w", g.Name, err)
		}
	}
	return nil
}

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

func importSkills(tx *sql.Tx, skills map[string]fleet.Skill) error {
	for id, s := range skills {
		id = fleet.NormalizeSkillName(id)
		fleet.NormalizeSkill(&s)
		if s.Name == "" {
			s.Name = id
		}
		if s.WorkspaceID == "" && s.Repo != "" {
			return fmt.Errorf("store import: skill %q repo scope requires workspace_id", id)
		}
		if id == "" || s.Name == "" {
			return fmt.Errorf("store import: skill requires id and name")
		}
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: skill %q: %w", id, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO skills (id, workspace_id, repo, name, prompt)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				workspace_id = excluded.workspace_id,
				repo = excluded.repo,
				name = excluded.name,
				prompt = excluded.prompt`,
			id, s.WorkspaceID, s.Repo, s.Name, s.Prompt,
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: skill name %q is already used by another skill in that scope", s.Name)
			}
			return fmt.Errorf("store import: upsert skill %s: %w", id, err)
		}
	}
	return nil
}

func importWorkspaces(tx *sql.Tx, workspaces []fleet.Workspace) error {
	for _, w := range workspaces {
		if w.ID == "" {
			id, err := derivedEntityID("", w.Name)
			if err != nil {
				return fmt.Errorf("store import: workspace %q: %w", w.Name, err)
			}
			w.ID = id
		}
		if w.ID == "" || w.Name == "" {
			return fmt.Errorf("store import: workspace requires id or name")
		}
		if err := validateEntityID(w.ID); err != nil {
			return fmt.Errorf("store import: workspace %q: %w", w.Name, err)
		}
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)`, w.ID).Scan(&exists); err != nil {
			return fmt.Errorf("store import: check workspace %s: %w", w.ID, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO workspaces (id, name, description, runner_image, updated_at)
			VALUES (?, ?, ?, ?, datetime('now'))
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				description = excluded.description,
				runner_image = excluded.runner_image,
				updated_at = datetime('now')`,
			w.ID, w.Name, w.Description, strings.TrimSpace(w.RunnerImage),
		); err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("store import: workspace name %q is already used by another workspace id", w.Name)
			}
			return fmt.Errorf("store import: upsert workspace %s: %w", w.ID, err)
		}
		if !exists {
			if err := seedWorkspaceGuardrails(tx, w.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedWorkspaceGuardrails(tx *sql.Tx, workspaceID string) error {
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
		SELECT ?, id, position, enabled
		FROM guardrails
		WHERE is_builtin = 1 AND workspace_id IS NULL AND repo IS NULL`,
		workspaceID,
	); err != nil {
		return fmt.Errorf("store import: seed workspace %s guardrails: %w", workspaceID, err)
	}
	return nil
}

func importWorkspaceGuardrails(tx *sql.Tx, workspaces []fleet.Workspace) error {
	for _, w := range workspaces {
		if w.Guardrails == nil {
			continue
		}
		workspaceID := strings.TrimSpace(w.ID)
		if workspaceID == "" {
			id, err := derivedEntityID("", w.Name)
			if err != nil {
				return fmt.Errorf("store import: workspace %q guardrails: %w", w.Name, err)
			}
			workspaceID = id
		} else {
			workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
		}
		// Omitted guardrails preserve existing refs; an explicit empty list clears them.
		if err := replaceWorkspaceGuardrailsTx(tx, workspaceID, w.Guardrails); err != nil {
			return err
		}
	}
	return nil
}

func replaceWorkspaceGuardrailsTx(tx *sql.Tx, workspaceID string, refs []fleet.WorkspaceGuardrailRef) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)`, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("store import: check workspace %s: %w", workspaceID, err)
	}
	if !exists {
		return &ErrValidation{Msg: fmt.Sprintf("workspace %q not found", workspaceID)}
	}
	if _, err := tx.Exec(`DELETE FROM workspace_guardrails WHERE workspace_id = ?`, workspaceID); err != nil {
		return fmt.Errorf("store import: replace workspace %s guardrails: %w", workspaceID, err)
	}
	seen := map[string]struct{}{}
	for i, ref := range refs {
		name := fleet.NormalizeGuardrailName(ref.GuardrailName)
		if name == "" {
			return &ErrValidation{Msg: fmt.Sprintf("workspace %q guardrail name is required", workspaceID)}
		}
		if _, ok := seen[name]; ok {
			return &ErrValidation{Msg: fmt.Sprintf("workspace %q references guardrail %q more than once", workspaceID, name)}
		}
		seen[name] = struct{}{}
		id, err := resolveWorkspaceGuardrailRef(tx, workspaceID, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ErrValidation{Msg: fmt.Sprintf("workspace %q references unknown guardrail %q", workspaceID, name)}
			}
			return fmt.Errorf("store import: validate workspace %s guardrail %s: %w", workspaceID, name, err)
		}
		position := ref.Position
		if position == 0 {
			position = i
		}
		if _, err := tx.Exec(`
			INSERT INTO workspace_guardrails (workspace_id, guardrail_name, position, enabled)
			VALUES (?, ?, ?, ?)`,
			workspaceID, id, position, boolToInt(ref.Enabled),
		); err != nil {
			return fmt.Errorf("store import: insert workspace %s guardrail %s: %w", workspaceID, name, err)
		}
	}
	return nil
}

func resolveWorkspaceGuardrailRef(q querier, workspaceID, ref string) (string, error) {
	ref = fleet.NormalizeGuardrailName(ref)
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	rows, err := q.Query(`
		SELECT id
		FROM guardrails
		WHERE repo IS NULL
		  AND (id = ? OR name = ?)
		  AND (workspace_id IS NULL OR workspace_id = ?)
		ORDER BY
			CASE WHEN id = ? THEN 0 ELSE 1 END,
			CASE WHEN workspace_id IS NULL THEN 0 ELSE 1 END`,
		ref, ref, workspaceID, ref,
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
	if len(ids) > 1 && ids[0] != ref {
		return "", fmt.Errorf("ambiguous guardrail %q in workspace %q; use guardrail id", ref, workspaceID)
	}
	return ids[0], nil
}

func importReferencedWorkspaces(tx *sql.Tx, agents []fleet.Agent, repos []fleet.Repo) error {
	seen := map[string]struct{}{fleet.DefaultWorkspaceID: {}}
	for _, a := range agents {
		id := strings.TrimSpace(a.WorkspaceID)
		if id == "" {
			id = fleet.DefaultWorkspaceID
		}
		seen[id] = struct{}{}
	}
	for _, r := range repos {
		id := strings.TrimSpace(r.WorkspaceID)
		if id == "" {
			id = fleet.DefaultWorkspaceID
		}
		seen[id] = struct{}{}
	}
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		if err := validateEntityID(id); err != nil {
			return fmt.Errorf("store import: workspace %q: %w", id, err)
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO workspaces (id, name, description, runner_image, updated_at)
			VALUES (?, ?, '', '', datetime('now'))`,
			id, workspaceNameFromID(id),
		)
		if err != nil {
			return fmt.Errorf("store import: ensure workspace %s: %w", id, err)
		}
		if inserted, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store import: check workspace %s insert result: %w", id, err)
		} else if inserted > 0 {
			if err := seedWorkspaceGuardrails(tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func workspaceNameFromID(id string) string {
	if id == fleet.DefaultWorkspaceID {
		return "Default"
	}
	return id
}

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

func importAgents(tx *sql.Tx, agents []fleet.Agent, updatePromptContent bool) error {
	for _, a := range agents {
		workspaceID := fleet.NormalizeWorkspaceID(a.WorkspaceID)
		scopeType := a.ScopeType
		if scopeType == "" {
			scopeType = "workspace"
		}
		promptID, err := ensureAgentPrompt(tx, a, updatePromptContent)
		if err != nil {
			return err
		}
		id, err := stableAgentID(tx, workspaceID, a)
		if err != nil {
			return err
		}
		skillRefs, err := resolveAgentSkillRefs(tx, a, workspaceID, scopeType)
		if err != nil {
			return err
		}
		skills, err := json.Marshal(skillRefs)
		if err != nil {
			return fmt.Errorf("store import: marshal agent %s skills: %w", a.Name, err)
		}
		canDispatch, err := json.Marshal(a.CanDispatch)
		if err != nil {
			return fmt.Errorf("store import: marshal agent %s can_dispatch: %w", a.Name, err)
		}
		allowPRs := boolToInt(a.AllowPRs)
		allowDispatch := boolToInt(a.AllowDispatch)
		allowMemory := bindingEnabledInt(a.AllowMemory)
		if _, err := tx.Exec(`
			INSERT INTO agents
			  (id,workspace_id,name,backend,model,skills,prompt,prompt_id,scope_type,scope_repo,allow_prs,allow_dispatch,can_dispatch,description,allow_memory)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id, name) DO UPDATE SET
				id = excluded.id,
				backend = excluded.backend,
				model = excluded.model,
				skills = excluded.skills,
				prompt = excluded.prompt,
				prompt_id = excluded.prompt_id,
				scope_type = excluded.scope_type,
				scope_repo = excluded.scope_repo,
				allow_prs = excluded.allow_prs,
				allow_dispatch = excluded.allow_dispatch,
				can_dispatch = excluded.can_dispatch,
				description = excluded.description,
				allow_memory = excluded.allow_memory`,
			id, workspaceID, a.Name, a.Backend, a.Model, string(skills), a.Prompt, promptID, scopeType, a.ScopeRepo,
			allowPRs, allowDispatch, string(canDispatch), a.Description, allowMemory,
		); err != nil {
			return fmt.Errorf("store import: upsert agent %s: %w", a.Name, err)
		}
	}
	return nil
}

func resolveAgentSkillRefs(tx *sql.Tx, a fleet.Agent, workspaceID, scopeType string) ([]string, error) {
	repo := ""
	if scopeType == "repo" {
		repo = a.ScopeRepo
	}
	refs := make([]string, 0, len(a.Skills))
	for _, raw := range a.Skills {
		ref := fleet.NormalizeSkillName(raw)
		id, err := resolveVisibleCatalogRef(tx, "skills", ref, workspaceID, repo)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if skill, ok, readErr := readSkillScopeByID(tx, ref); readErr != nil {
					return nil, fmt.Errorf("store import: read skill %s for agent %s: %w", ref, a.Name, readErr)
				} else if ok {
					if skill.Repo != "" && repo == "" {
						return nil, fmt.Errorf("workspace-scoped agent %q references repo-scoped skill %q without repo context", a.Name, ref)
					}
					return nil, fmt.Errorf("agent %q references skill %q outside its visible catalog scope", a.Name, ref)
				}
				return nil, fmt.Errorf("store import: agent %s references unknown skill %q", a.Name, ref)
			}
			return nil, fmt.Errorf("store import: resolve skill %s for agent %s: %w", ref, a.Name, err)
		}
		refs = append(refs, id)
	}
	return refs, nil
}

func readSkillScopeByID(q querier, id string) (fleet.Skill, bool, error) {
	var skill fleet.Skill
	err := q.QueryRow("SELECT id, COALESCE(workspace_id, ''), COALESCE(repo, ''), name FROM skills WHERE id = ?", id).
		Scan(&skill.ID, &skill.WorkspaceID, &skill.Repo, &skill.Name)
	if err == nil {
		return skill, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fleet.Skill{}, false, nil
	}
	return fleet.Skill{}, false, err
}

func ensureAgentPrompt(tx *sql.Tx, a fleet.Agent, updatePromptContent bool) (string, error) {
	if a.PromptID != "" {
		scopeRepo := ""
		if a.ScopeType == "repo" {
			scopeRepo = a.ScopeRepo
		}
		if _, err := resolveVisibleCatalogID(tx, "prompts", a.PromptID, a.WorkspaceID, scopeRepo); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("store import: agent %s references unknown prompt_id %q", a.Name, a.PromptID)
			}
			return "", fmt.Errorf("store import: read prompt_id %s for agent %s: %w", a.PromptID, a.Name, err)
		}
		return a.PromptID, nil
	}
	name := a.PromptRef
	if name == "" {
		name = a.Name
	}
	if name == "" {
		return "", fmt.Errorf("store import: agent prompt requires agent name or prompt_ref")
	}
	content := a.Prompt
	if content == "" && a.PromptRef != "" {
		scopeRepo := ""
		if a.ScopeType == "repo" {
			scopeRepo = a.ScopeRepo
		}
		existingID, err := resolveAgentPromptRef(tx, name, a.PromptScope, a.WorkspaceID, scopeRepo)
		if err == nil {
			return existingID, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("store import: agent %s references unknown prompt_ref %q", a.Name, a.PromptRef)
		}
		return "", fmt.Errorf("store import: resolve prompt_ref %s for agent %s: %w", name, a.Name, err)
	}
	if !updatePromptContent && content != "" {
		var existingID string
		err := queryPromptByScopeName(tx, "", "", name).Scan(&existingID)
		if err == nil {
			return existingID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("store import: read prompt %s: %w", name, err)
		}
	}
	id, err := derivedEntityID("prompt_", name)
	if err != nil {
		return "", fmt.Errorf("store import: prompt %q for agent %s: %w", name, a.Name, err)
	}
	if _, err := tx.Exec(`
		INSERT INTO prompts (id, name, description, content, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			content = CASE WHEN ? THEN excluded.content ELSE prompts.content END,
			updated_at = CASE WHEN ? THEN datetime('now') ELSE prompts.updated_at END`,
		id, name, "Migrated prompt for agent "+a.Name, content,
		updatePromptContent, updatePromptContent,
	); err != nil {
		return "", fmt.Errorf("store import: upsert prompt %s for agent %s: %w", name, a.Name, err)
	}
	if err := queryPromptByScopeName(tx, "", "", name).Scan(&id); err != nil {
		return "", fmt.Errorf("store import: read prompt %s id: %w", name, err)
	}
	return id, nil
}

func queryPromptByScopeName(q querier, workspaceID, repo, name string) *sql.Row {
	return queryCatalogIDByScopeName(q, "prompts", workspaceID, repo, name)
}

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

func resolveVisibleCatalogRef(q querier, table, ref, workspaceID, repo string) (string, error) {
	id, err := resolveVisibleCatalogID(q, table, ref, workspaceID, repo)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return resolveVisibleCatalogName(q, table, strings.TrimSuffix(table, "s"), strings.TrimSuffix(table, "s")+" id", ref, workspaceID, repo)
}

func resolveVisibleCatalogID(q querier, table, id, workspaceID, repo string) (string, error) {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	repo = fleet.NormalizeRepoName(repo)
	rows, err := q.Query(`
		SELECT id
		FROM `+table+`
		WHERE id = ?
		  AND (
			(workspace_id IS NULL AND repo IS NULL)
			OR (workspace_id = ? AND repo IS NULL)
			OR (? <> '' AND workspace_id = ? AND repo = ?)
		)
		LIMIT 1`,
		id, workspaceID, repo, workspaceID, repo,
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

func stableAgentID(q querier, workspaceID string, a fleet.Agent) (string, error) {
	var existing string
	err := q.QueryRow("SELECT COALESCE(id, '') FROM agents WHERE workspace_id=? AND name=?", workspaceID, a.Name).Scan(&existing)
	if err == nil && existing != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store import: read agent %s id: %w", a.Name, err)
	}
	return newAgentID()
}

func newAgentID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store import: generate agent id: %w", err)
	}
	return "agent_" + hex.EncodeToString(b[:]), nil
}

func importRepos(tx *sql.Tx, repos []fleet.Repo) error {
	for _, r := range repos {
		workspaceID := fleet.NormalizeWorkspaceID(r.WorkspaceID)
		enabled := boolToInt(r.Enabled)
		if _, err := tx.Exec(
			`INSERT INTO repos(name,workspace_id,enabled) VALUES(?,?,?)
			ON CONFLICT(workspace_id, name) DO UPDATE SET enabled = excluded.enabled`,
			r.Name, workspaceID, enabled,
		); err != nil {
			return fmt.Errorf("store import: upsert repo %s: %w", r.Name, err)
		}
		// Delete and re-insert bindings so that re-importing the same YAML
		// does not accumulate duplicate rows. A repo's binding list is treated
		// as a whole (replace-all semantics): remove what was there, write
		// what the new config says.
		if _, err := tx.Exec("DELETE FROM bindings WHERE workspace_id=? AND repo=?", workspaceID, r.Name); err != nil {
			return fmt.Errorf("store import: clear bindings for repo %s: %w", r.Name, err)
		}
		for _, b := range r.Use {
			labels, err := json.Marshal(b.Labels)
			if err != nil {
				return fmt.Errorf("store import: marshal binding labels for repo %s agent %s: %w", r.Name, b.Agent, err)
			}
			events, err := json.Marshal(b.Events)
			if err != nil {
				return fmt.Errorf("store import: marshal binding events for repo %s agent %s: %w", r.Name, b.Agent, err)
			}
			bindingEnabled := bindingEnabledInt(b.Enabled)
			if _, err := tx.Exec(`
				INSERT INTO bindings(workspace_id,repo,agent,labels,events,cron,enabled)
				VALUES (?,?,?,?,?,?,?)`,
				workspaceID, r.Name, b.Agent, string(labels), string(events), b.Cron, bindingEnabled,
			); err != nil {
				return fmt.Errorf("store import: insert binding repo %s agent %s: %w", r.Name, b.Agent, err)
			}
		}
	}
	return nil
}

// Load reads all configuration from the database and returns a *config.Config
// ready for use. It applies the same defaults, normalization, secret resolution
// and validation as config.Load does for YAML.
func Load(db *sql.DB) (*config.Config, error) {
	cfg := &config.Config{}

	if err := loadBackends(db, cfg); err != nil {
		return nil, err
	}
	if err := loadRuntimeSettings(db, cfg); err != nil {
		return nil, err
	}
	if err := loadSkills(db, cfg); err != nil {
		return nil, err
	}
	if err := loadWorkspaces(db, cfg); err != nil {
		return nil, err
	}
	if err := loadPrompts(db, cfg); err != nil {
		return nil, err
	}
	if err := loadAgents(db, cfg); err != nil {
		return nil, err
	}
	if err := loadRepos(db, cfg); err != nil {
		return nil, err
	}
	if err := loadGuardrails(db, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadGuardrails(db *sql.DB, cfg *config.Config) error {
	rows, err := ReadAllGuardrails(db)
	if err != nil {
		return fmt.Errorf("store load: read guardrails: %w", err)
	}
	cfg.Guardrails = rows
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

func loadSkills(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT id,COALESCE(workspace_id, ''),COALESCE(repo, ''),name,prompt FROM skills")
	if err != nil {
		return fmt.Errorf("store load: query skills: %w", err)
	}
	defer rows.Close()

	skills := make(map[string]fleet.Skill)
	for rows.Next() {
		var id string
		var skill fleet.Skill
		if err := rows.Scan(&id, &skill.WorkspaceID, &skill.Repo, &skill.Name, &skill.Prompt); err != nil {
			return fmt.Errorf("store load: scan skill: %w", err)
		}
		skill.ID = id
		skills[id] = skill
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate skills: %w", err)
	}
	cfg.Skills = skills
	return nil
}

func loadWorkspaces(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT id,name,description,runner_image FROM workspaces ORDER BY name")
	if err != nil {
		return fmt.Errorf("store load: query workspaces: %w", err)
	}
	defer rows.Close()
	var workspaces []fleet.Workspace
	for rows.Next() {
		var w fleet.Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.RunnerImage); err != nil {
			return fmt.Errorf("store load: scan workspace: %w", err)
		}
		workspaces = append(workspaces, w)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store load: close workspaces: %w", err)
	}
	for i := range workspaces {
		refs, err := readWorkspaceGuardrails(db, workspaces[i].ID)
		if err != nil {
			return err
		}
		workspaces[i].Guardrails = refs
	}
	cfg.Workspaces = workspaces
	return nil
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

func loadAgents(db querier, cfg *config.Config) error {
	rows, err := db.Query(`
		SELECT a.id,a.workspace_id,a.name,a.backend,a.model,a.skills,COALESCE(p.content, a.prompt),a.prompt_id,COALESCE(p.name, ''),COALESCE(p.workspace_id, ''),COALESCE(p.repo, ''),a.scope_type,a.scope_repo,a.allow_prs,a.allow_dispatch,a.can_dispatch,a.description,a.allow_memory
		FROM agents a
		LEFT JOIN prompts p ON p.id = a.prompt_id
		ORDER BY a.name`)
	if err != nil {
		return fmt.Errorf("store load: query agents: %w", err)
	}
	defer rows.Close()

	var agents []fleet.Agent
	for rows.Next() {
		var id, workspaceID, name, backend, model, skillsJSON, prompt, promptID, promptRef, promptWorkspace, promptRepo, scopeType, scopeRepo, canDispatchJSON, description string
		var allowPRs, allowDispatch, allowMemory int
		if err := rows.Scan(
			&id, &workspaceID, &name, &backend, &model, &skillsJSON, &prompt, &promptID, &promptRef, &promptWorkspace, &promptRepo, &scopeType, &scopeRepo,
			&allowPRs, &allowDispatch, &canDispatchJSON, &description, &allowMemory,
		); err != nil {
			return fmt.Errorf("store load: scan agent: %w", err)
		}
		var skills []string
		if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
			return fmt.Errorf("store load: parse agent %s skills: %w", name, err)
		}
		var canDispatch []string
		if err := json.Unmarshal([]byte(canDispatchJSON), &canDispatch); err != nil {
			return fmt.Errorf("store load: parse agent %s can_dispatch: %w", name, err)
		}
		// Always materialise AllowMemory as a non-nil pointer so downstream
		// readers see a concrete bool reflecting the stored row, not the
		// "absent" sentinel that nil represents on inbound YAML/JSON paths.
		allowMem := intToBool(allowMemory)
		promptScope := ""
		if promptID != "" {
			promptScope = fleet.CatalogScopePath(promptWorkspace, promptRepo)
		}
		agents = append(agents, fleet.Agent{
			ID:            id,
			WorkspaceID:   workspaceID,
			Name:          name,
			Backend:       backend,
			Model:         model,
			Skills:        skills,
			Prompt:        prompt,
			PromptID:      promptID,
			PromptRef:     promptRef,
			PromptScope:   promptScope,
			ScopeType:     scopeType,
			ScopeRepo:     scopeRepo,
			AllowPRs:      intToBool(allowPRs),
			AllowDispatch: intToBool(allowDispatch),
			CanDispatch:   canDispatch,
			Description:   description,
			AllowMemory:   &allowMem,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate agents: %w", err)
	}
	cfg.Agents = agents
	return nil
}

func loadRepos(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT workspace_id,name,enabled FROM repos ORDER BY workspace_id, name")
	if err != nil {
		return fmt.Errorf("store load: query repos: %w", err)
	}
	defer rows.Close()

	var repos []fleet.Repo
	for rows.Next() {
		var workspaceID, name string
		var enabled int
		if err := rows.Scan(&workspaceID, &name, &enabled); err != nil {
			return fmt.Errorf("store load: scan repo: %w", err)
		}
		repos = append(repos, fleet.Repo{WorkspaceID: workspaceID, Name: name, Enabled: intToBool(enabled)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate repos: %w", err)
	}

	// Load bindings for each repo.
	for i := range repos {
		bindings, err := loadBindingsForRepo(db, repos[i].WorkspaceID, repos[i].Name)
		if err != nil {
			return err
		}
		repos[i].Use = bindings
	}
	cfg.Repos = repos
	return nil
}

func loadBindingsForRepo(db querier, workspaceID, repo string) ([]fleet.Binding, error) {
	rows, err := db.Query(
		"SELECT id,agent,labels,events,cron,enabled FROM bindings WHERE workspace_id=? AND repo=? ORDER BY id", fleet.NormalizeWorkspaceID(workspaceID), repo,
	)
	if err != nil {
		return nil, fmt.Errorf("store load: query bindings for %s: %w", repo, err)
	}
	defer rows.Close()

	var bindings []fleet.Binding
	for rows.Next() {
		var id int64
		var agent, labelsJSON, eventsJSON, cron string
		var enabled int
		if err := rows.Scan(&id, &agent, &labelsJSON, &eventsJSON, &cron, &enabled); err != nil {
			return nil, fmt.Errorf("store load: scan binding for %s: %w", repo, err)
		}
		var labels []string
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
			return nil, fmt.Errorf("store load: parse binding labels for %s: %w", repo, err)
		}
		var events []string
		if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
			return nil, fmt.Errorf("store load: parse binding events for %s: %w", repo, err)
		}
		b := fleet.Binding{
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
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store load: iterate bindings for %s: %w", repo, err)
	}
	return bindings, nil
}

// ReadMemory returns the stored memory string, a found flag, and the
// last-updated timestamp for (workspace, agent, repo). If no row exists, it returns
// ("", false, time.Time{}, nil). An empty content string with found=true
// means the agent intentionally cleared its memory.
func ReadMemory(db *sql.DB, workspace, agent, repo string) (string, bool, time.Time, error) {
	var content, updatedAt string
	err := db.QueryRow(
		"SELECT content, updated_at FROM memory WHERE workspace_id=? AND agent=? AND repo=?", workspace, agent, repo,
	).Scan(&content, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, time.Time{}, nil
	}
	if err != nil {
		return "", false, time.Time{}, fmt.Errorf("store: read memory %s/%s/%s: %w", workspace, agent, repo, err)
	}
	// The modernc.org/sqlite driver returns TIMESTAMP columns as RFC3339 strings
	// (e.g. "2026-04-21T10:30:00Z"). Parse with time.RFC3339 and fall back to
	// the bare "YYYY-MM-DD HH:MM:SS" SQLite text format as a safety net.
	t, parseErr := time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		t, _ = time.Parse(time.DateTime, updatedAt)
	}
	return content, true, t.UTC(), nil
}

// WriteMemory upserts the memory string for (workspace, agent, repo), setting updated_at
// to the current UTC timestamp.
func WriteMemory(db *sql.DB, workspace, agent, repo, content string) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO memory(workspace_id,agent,repo,content,updated_at) VALUES(?,?,?,?,datetime('now'))`,
		workspace, agent, repo, content,
	)
	if err != nil {
		return fmt.Errorf("store: write memory %s/%s/%s: %w", workspace, agent, repo, err)
	}
	return nil
}

// boolToInt converts a bool to 0/1 for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// intToBool converts a SQLite 0/1 to bool.
func intToBool(i int) bool { return i != 0 }

// bindingEnabledInt converts a nullable *bool flag to 0/1 for SQLite storage.
// Nil means the default (enabled); only an explicit non-nil false maps to 0.
// Used for both binding.Enabled and agent.AllowMemory, which share this
// nil-means-default-on semantics.
func bindingEnabledInt(enabled *bool) int {
	if enabled != nil && !*enabled {
		return 0
	}
	return 1
}

// ImportCount holds row counts written during an Import call, for progress
// logging.
type ImportCount struct {
	Backends int
	Skills   int
	Agents   int
	Repos    int
	Bindings int
}

// String returns a human-readable summary of imported row counts.
func (c ImportCount) String() string {
	return fmt.Sprintf("imported %d backends, %d skills, %d agents, %d repos, %d bindings",
		c.Backends, c.Skills, c.Agents, c.Repos, c.Bindings)
}

// CountFrom returns an ImportCount reflecting the current row counts in db.
func CountFrom(db *sql.DB) (ImportCount, error) {
	var c ImportCount
	tables := []struct {
		table string
		dest  *int
	}{
		{"backends", &c.Backends},
		{"skills", &c.Skills},
		{"agents", &c.Agents},
		{"repos", &c.Repos},
		{"bindings", &c.Bindings},
	}
	for _, t := range tables {
		if err := db.QueryRow("SELECT COUNT(*) FROM " + t.table).Scan(t.dest); err != nil {
			return c, fmt.Errorf("store: count %s: %w", t.table, err)
		}
	}
	return c, nil
}

// LoadAndValidate is the full startup path when --db is used. It reads the
// config from the database, resolves secrets from env vars, and runs the same
// validation as config.Load. The returned *config.Config is ready to use.
func LoadAndValidate(db *sql.DB) (*config.Config, error) {
	cfg, err := Load(db)
	if err != nil {
		return nil, err
	}
	return config.FinishLoad(cfg)
}
