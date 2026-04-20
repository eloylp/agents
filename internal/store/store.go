// Package store persists the daemon configuration in a SQLite database.
// It provides Import to write a parsed config.Config into the database and
// LoadConfig to read it back. Secrets (webhook secret, API keys) are never
// stored; only the environment-variable names are persisted so that
// config.Config.Finalize() can resolve them at startup.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/eloylp/agents/internal/config"
)

//go:embed migrations/001_initial.sql
var schema string

// Store wraps a SQLite database holding the full daemon configuration.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
// Use ":memory:" as path for an in-process test database.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Import writes all entities from cfg into the database, replacing any
// previously stored data (replace semantics: all tables are cleared first).
// cfg must already be a fully-resolved config (i.e. the result of config.Load
// or a config on which config.Config.Finalize has been called), so that
// prompts are inline and defaults have been applied.
func (s *Store) Import(cfg *config.Config) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin import transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, table := range []string{"bindings", "repos", "agents", "skills", "backends", "config"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}

	if err := importBackends(tx, cfg); err != nil {
		return err
	}
	if err := importSkills(tx, cfg); err != nil {
		return err
	}
	if err := importAgents(tx, cfg); err != nil {
		return err
	}
	if err := importReposAndBindings(tx, cfg); err != nil {
		return err
	}
	if err := importDaemonConfig(tx, cfg); err != nil {
		return err
	}

	return tx.Commit()
}

func importBackends(tx *sql.Tx, cfg *config.Config) error {
	for name, b := range cfg.Daemon.AIBackends {
		args, err := json.Marshal(b.Args)
		if err != nil {
			return fmt.Errorf("store: marshal backend %s args: %w", name, err)
		}
		env, err := json.Marshal(b.Env)
		if err != nil {
			return fmt.Errorf("store: marshal backend %s env: %w", name, err)
		}
		_, err = tx.Exec(
			`INSERT INTO backends VALUES (?,?,?,?,?,?,?)`,
			name, b.Command, string(args), string(env),
			b.TimeoutSeconds, b.MaxPromptChars, b.RedactionSaltEnv,
		)
		if err != nil {
			return fmt.Errorf("store: insert backend %s: %w", name, err)
		}
	}
	return nil
}

func importSkills(tx *sql.Tx, cfg *config.Config) error {
	for name, skill := range cfg.Skills {
		if _, err := tx.Exec(`INSERT INTO skills VALUES (?,?)`, name, skill.Prompt); err != nil {
			return fmt.Errorf("store: insert skill %s: %w", name, err)
		}
	}
	return nil
}

func importAgents(tx *sql.Tx, cfg *config.Config) error {
	for _, a := range cfg.Agents {
		skills, err := json.Marshal(a.Skills)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s skills: %w", a.Name, err)
		}
		canDispatch, err := json.Marshal(a.CanDispatch)
		if err != nil {
			return fmt.Errorf("store: marshal agent %s can_dispatch: %w", a.Name, err)
		}
		_, err = tx.Exec(
			`INSERT INTO agents VALUES (?,?,?,?,?,?,?,?)`,
			a.Name, a.Backend, string(skills), a.Prompt,
			boolToInt(a.AllowPRs), boolToInt(a.AllowDispatch),
			string(canDispatch), a.Description,
		)
		if err != nil {
			return fmt.Errorf("store: insert agent %s: %w", a.Name, err)
		}
	}
	return nil
}

func importReposAndBindings(tx *sql.Tx, cfg *config.Config) error {
	for _, r := range cfg.Repos {
		if _, err := tx.Exec(`INSERT INTO repos VALUES (?,?)`, r.Name, boolToInt(r.Enabled)); err != nil {
			return fmt.Errorf("store: insert repo %s: %w", r.Name, err)
		}
		for _, b := range r.Use {
			labels, err := json.Marshal(b.Labels)
			if err != nil {
				return fmt.Errorf("store: marshal binding labels for repo %s: %w", r.Name, err)
			}
			events, err := json.Marshal(b.Events)
			if err != nil {
				return fmt.Errorf("store: marshal binding events for repo %s: %w", r.Name, err)
			}
			_, err = tx.Exec(
				`INSERT INTO bindings (repo,agent,labels,events,cron,enabled) VALUES (?,?,?,?,?,?)`,
				r.Name, b.Agent, string(labels), string(events), b.Cron, boolToInt(b.IsEnabled()),
			)
			if err != nil {
				return fmt.Errorf("store: insert binding %s->%s: %w", r.Name, b.Agent, err)
			}
		}
	}
	return nil
}

func importDaemonConfig(tx *sql.Tx, cfg *config.Config) error {
	// Each daemon config section is stored as a JSON blob in the config table.
	// Resolved secrets (WebhookSecret, APIKey) are excluded via json:"-" tags
	// on those fields; only the *Env names are persisted.
	sections := []struct {
		key   string
		value any
	}{
		{"http", cfg.Daemon.HTTP},
		{"log", cfg.Daemon.Log},
		{"processor", cfg.Daemon.Processor},
		{"proxy", cfg.Daemon.Proxy},
		{"memory_dir", cfg.Daemon.MemoryDir},
	}
	for _, s := range sections {
		var v string
		switch val := s.value.(type) {
		case string:
			v = val
		default:
			b, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("store: marshal config section %s: %w", s.key, err)
			}
			v = string(b)
		}
		if _, err := tx.Exec(`INSERT INTO config VALUES (?,?)`, s.key, v); err != nil {
			return fmt.Errorf("store: insert config %s: %w", s.key, err)
		}
	}
	return nil
}

// LoadConfig reads all stored entities from the database and returns a
// fully-initialised *config.Config ready for use. Finalize is called
// internally to apply defaults, normalise values, resolve secrets from the
// environment, and validate the result.
func (s *Store) LoadConfig() (*config.Config, error) {
	cfg := &config.Config{}

	if err := loadBackends(s.db, cfg); err != nil {
		return nil, err
	}
	if err := loadSkills(s.db, cfg); err != nil {
		return nil, err
	}
	if err := loadAgents(s.db, cfg); err != nil {
		return nil, err
	}
	if err := loadReposAndBindings(s.db, cfg); err != nil {
		return nil, err
	}
	if err := loadDaemonConfig(s.db, cfg); err != nil {
		return nil, err
	}

	if err := cfg.Finalize(); err != nil {
		return nil, fmt.Errorf("store: invalid configuration in database: %w", err)
	}
	return cfg, nil
}

func loadBackends(db *sql.DB, cfg *config.Config) error {
	rows, err := db.Query(`SELECT name,command,args,env,timeout_seconds,max_prompt_chars,redaction_salt_env FROM backends`)
	if err != nil {
		return fmt.Errorf("store: query backends: %w", err)
	}
	defer rows.Close()

	cfg.Daemon.AIBackends = make(map[string]config.AIBackendConfig)
	for rows.Next() {
		var name, command, argsJSON, envJSON, saltEnv string
		var timeout, maxPrompt int
		if err := rows.Scan(&name, &command, &argsJSON, &envJSON, &timeout, &maxPrompt, &saltEnv); err != nil {
			return fmt.Errorf("store: scan backend: %w", err)
		}
		var args []string
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Errorf("store: unmarshal backend %s args: %w", name, err)
		}
		var env map[string]string
		if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
			return fmt.Errorf("store: unmarshal backend %s env: %w", name, err)
		}
		cfg.Daemon.AIBackends[name] = config.AIBackendConfig{
			Command:          command,
			Args:             args,
			Env:              env,
			TimeoutSeconds:   timeout,
			MaxPromptChars:   maxPrompt,
			RedactionSaltEnv: saltEnv,
		}
	}
	return rows.Err()
}

func loadSkills(db *sql.DB, cfg *config.Config) error {
	rows, err := db.Query(`SELECT name,prompt FROM skills`)
	if err != nil {
		return fmt.Errorf("store: query skills: %w", err)
	}
	defer rows.Close()

	cfg.Skills = make(map[string]config.SkillDef)
	for rows.Next() {
		var name, prompt string
		if err := rows.Scan(&name, &prompt); err != nil {
			return fmt.Errorf("store: scan skill: %w", err)
		}
		cfg.Skills[name] = config.SkillDef{Prompt: prompt}
	}
	return rows.Err()
}

func loadAgents(db *sql.DB, cfg *config.Config) error {
	rows, err := db.Query(`SELECT name,backend,skills,prompt,allow_prs,allow_dispatch,can_dispatch,description FROM agents`)
	if err != nil {
		return fmt.Errorf("store: query agents: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, backend, skillsJSON, prompt, canDispatchJSON, description string
		var allowPRs, allowDispatch int
		if err := rows.Scan(&name, &backend, &skillsJSON, &prompt, &allowPRs, &allowDispatch, &canDispatchJSON, &description); err != nil {
			return fmt.Errorf("store: scan agent: %w", err)
		}
		var skills []string
		if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
			return fmt.Errorf("store: unmarshal agent %s skills: %w", name, err)
		}
		var canDispatch []string
		if err := json.Unmarshal([]byte(canDispatchJSON), &canDispatch); err != nil {
			return fmt.Errorf("store: unmarshal agent %s can_dispatch: %w", name, err)
		}
		cfg.Agents = append(cfg.Agents, config.AgentDef{
			Name:          name,
			Backend:       backend,
			Skills:        skills,
			Prompt:        prompt,
			AllowPRs:      intToBool(allowPRs),
			AllowDispatch: intToBool(allowDispatch),
			CanDispatch:   canDispatch,
			Description:   description,
		})
	}
	return rows.Err()
}

func loadReposAndBindings(db *sql.DB, cfg *config.Config) error {
	rows, err := db.Query(`SELECT name,enabled FROM repos`)
	if err != nil {
		return fmt.Errorf("store: query repos: %w", err)
	}
	defer rows.Close()

	repoMap := make(map[string]*config.RepoDef)
	for rows.Next() {
		var name string
		var enabled int
		if err := rows.Scan(&name, &enabled); err != nil {
			return fmt.Errorf("store: scan repo: %w", err)
		}
		r := &config.RepoDef{Name: name, Enabled: intToBool(enabled)}
		repoMap[name] = r
		cfg.Repos = append(cfg.Repos, *r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	brows, err := db.Query(`SELECT repo,agent,labels,events,cron,enabled FROM bindings ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store: query bindings: %w", err)
	}
	defer brows.Close()

	// reindex: re-build cfg.Repos slice with bindings
	repoBindings := make(map[string][]config.Binding)
	for brows.Next() {
		var repo, agent, labelsJSON, eventsJSON, cron string
		var enabled int
		if err := brows.Scan(&repo, &agent, &labelsJSON, &eventsJSON, &cron, &enabled); err != nil {
			return fmt.Errorf("store: scan binding: %w", err)
		}
		var labels []string
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
			return fmt.Errorf("store: unmarshal binding labels for repo %s: %w", repo, err)
		}
		var events []string
		if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
			return fmt.Errorf("store: unmarshal binding events for repo %s: %w", repo, err)
		}
		b := config.Binding{
			Agent:  agent,
			Labels: labels,
			Events: events,
			Cron:   cron,
		}
		if !intToBool(enabled) {
			f := false
			b.Enabled = &f
		}
		repoBindings[repo] = append(repoBindings[repo], b)
	}
	if err := brows.Err(); err != nil {
		return err
	}

	// Attach bindings to repos.
	for i := range cfg.Repos {
		cfg.Repos[i].Use = repoBindings[cfg.Repos[i].Name]
	}
	return nil
}

func loadDaemonConfig(db *sql.DB, cfg *config.Config) error {
	rows, err := db.Query(`SELECT key,value FROM config`)
	if err != nil {
		return fmt.Errorf("store: query config: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("store: scan config: %w", err)
		}
		switch key {
		case "http":
			if err := json.Unmarshal([]byte(value), &cfg.Daemon.HTTP); err != nil {
				return fmt.Errorf("store: unmarshal config http: %w", err)
			}
		case "log":
			if err := json.Unmarshal([]byte(value), &cfg.Daemon.Log); err != nil {
				return fmt.Errorf("store: unmarshal config log: %w", err)
			}
		case "processor":
			if err := json.Unmarshal([]byte(value), &cfg.Daemon.Processor); err != nil {
				return fmt.Errorf("store: unmarshal config processor: %w", err)
			}
		case "proxy":
			if err := json.Unmarshal([]byte(value), &cfg.Daemon.Proxy); err != nil {
				return fmt.Errorf("store: unmarshal config proxy: %w", err)
			}
		case "memory_dir":
			cfg.Daemon.MemoryDir = value
		}
	}
	return rows.Err()
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool {
	return i != 0
}
