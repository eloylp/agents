package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

func importRepos(tx *sql.Tx, repos []fleet.Repo) error {
	if err := importRepoRows(tx, repos); err != nil {
		return err
	}
	return importRepoBindings(tx, repos)
}

func importRepoRows(tx *sql.Tx, repos []fleet.Repo) error {
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
	}
	return nil
}

func importRepoBindings(tx *sql.Tx, repos []fleet.Repo) error {
	for _, r := range repos {
		workspaceID := fleet.NormalizeWorkspaceID(r.WorkspaceID)
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

// ──── Repos ────────────────────────────────────────────────────────────────────────────────────────

// ReadRepos returns all repos (with bindings) from the database.
func ReadRepos(db *sql.DB) ([]fleet.Repo, error) {
	var cfg config.Config
	if err := loadRepos(db, &cfg); err != nil {
		return nil, err
	}
	return cfg.Repos, nil
}

// UpsertRepo inserts or replaces a repo and its bindings. Bindings are
// replaced wholesale: any existing bindings for the repo are removed before
// the new list is written. The repo name and binding agents/events are
// normalized (trimmed / lowercased) before writing.
//
// This non-Tx wrapper is retained for compatibility with store-level tests and
// setup helpers. Production mutation paths should call internal/service, which
// owns the transaction and post-write fleet validation, or use UpsertRepoTx
// inside a service-owned transaction.
func UpsertRepo(db *sql.DB, r fleet.Repo) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert repo %s: begin: %w", r.Name, err)
	}
	defer tx.Rollback()
	if err := UpsertRepoTx(tx, r); err != nil {
		return err
	}
	if err := validateFleet(tx); err != nil {
		return &ErrValidation{Msg: fmt.Sprintf("store: upsert repo %s: %v", r.Name, err)}
	}
	if err := validateCronExpressions([]fleet.Repo{r}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertRepoTx persists one normalized repo and replaces its bindings inside
// an existing transaction. Callers own the surrounding commit.
func UpsertRepoTx(tx *sql.Tx, r fleet.Repo) error {
	fleet.NormalizeRepo(&r)
	if err := importRepos(tx, []fleet.Repo{r}); err != nil {
		return err
	}
	return nil
}

// DeleteRepo removes a repo and all of its bindings. Deleting the last enabled
// (or only) repo is allowed, see issue #302; the daemon runs cleanly with zero
// enabled repos.
func DeleteRepo(db *sql.DB, name string) error {
	return DeleteWorkspaceRepo(db, fleet.DefaultWorkspaceID, name)
}

func DeleteWorkspaceRepo(db *sql.DB, workspaceID, name string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete repo %s: begin: %w", name, err)
	}
	defer tx.Rollback()
	if err := DeleteWorkspaceRepoTx(tx, workspaceID, name); err != nil {
		return err
	}
	return tx.Commit()
}

func DeleteWorkspaceRepoTx(tx *sql.Tx, workspaceID, name string) error {
	workspaceID = fleet.NormalizeWorkspaceID(workspaceID)
	name = fleet.NormalizeRepoName(name)
	if refs, err := repoConfigReferences(tx, workspaceID, name); err != nil {
		return err
	} else if len(refs) > 0 {
		return &ErrConflict{Msg: fmt.Sprintf("repo %q in workspace %q is referenced by %s", name, workspaceID, strings.Join(refs, ", "))}
	}
	if _, err := tx.Exec("DELETE FROM bindings WHERE workspace_id=? AND repo=?", workspaceID, name); err != nil {
		return fmt.Errorf("store: delete bindings for repo %s: %w", name, err)
	}
	if _, err := tx.Exec("DELETE FROM repos WHERE workspace_id=? AND name=?", workspaceID, name); err != nil {
		return fmt.Errorf("store: delete repo %s: %w", name, err)
	}
	return nil
}

func repoConfigReferences(q querier, workspaceID, repo string) ([]string, error) {
	checks := []struct {
		label string
		sql   string
		args  []any
	}{
		{"repo-scoped agents", "SELECT COUNT(*) FROM agents WHERE workspace_id=? AND scope_type='repo' AND scope_repo=?", []any{workspaceID, repo}},
		{"prompts", "SELECT COUNT(*) FROM prompts WHERE workspace_id=? AND repo=?", []any{workspaceID, repo}},
		{"skills", "SELECT COUNT(*) FROM skills WHERE workspace_id=? AND repo=?", []any{workspaceID, repo}},
		{"guardrails", "SELECT COUNT(*) FROM guardrails WHERE workspace_id=? AND repo=?", []any{workspaceID, repo}},
		{"token budgets", "SELECT COUNT(*) FROM token_budgets WHERE workspace_id=? AND repo=?", []any{workspaceID, repo}},
	}
	var refs []string
	for _, check := range checks {
		var n int
		if err := q.QueryRow(check.sql, check.args...).Scan(&n); err != nil {
			return nil, fmt.Errorf("store: check repo %s references: %w", check.label, err)
		}
		if n > 0 {
			refs = append(refs, fmt.Sprintf("%d %s", n, check.label))
		}
	}
	return refs, nil
}

func EnableWorkspaceRepoTx(tx *sql.Tx, workspace, name string, enabled bool) error {
	res, err := tx.Exec("UPDATE repos SET enabled=? WHERE workspace_id=? AND name=?", boolToInt(enabled), fleet.NormalizeWorkspaceID(workspace), fleet.NormalizeRepoName(name))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return &ErrNotFound{Msg: fmt.Sprintf("repo %q not found", name)}
	}
	return nil
}
