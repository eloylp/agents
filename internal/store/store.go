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
//	// Subsequent starts — read from DB:
//	cfg, err = store.Load(db)
package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

//go:embed migrations/*.sql
var migrationsFS embed.FS

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

// migrate applies all embedded SQL migration files in lexicographic order.
// Each file is applied as a single transaction; already-applied files are
// tracked via a schema_migrations table.
func migrate(db *sql.DB) error {
	// Ensure the migrations tracking table exists.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations dir: %w", err)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		var applied bool
		row := db.QueryRow("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=?)", name)
		if err := row.Scan(&applied); err != nil {
			return fmt.Errorf("store: check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(name) VALUES(?)", name); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %s: %w", name, err)
		}
	}
	return nil
}

// Import writes cfg into the database, upserting every entity. Existing rows
// are replaced (INSERT OR REPLACE). Prompts are stored inline — any
// prompt_file references must be resolved in cfg before calling Import (i.e.
// pass the output of config.Load which resolves them eagerly).
//
// Secrets (WebhookSecret) are NOT written — only the env-var name
// (WebhookSecretEnv) is stored. The secret is re-resolved from the
// environment at Load time.
func Import(db *sql.DB, cfg *config.Config) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store import: begin: %w", err)
	}
	defer tx.Rollback()

	if err := importDaemon(tx, cfg.Daemon); err != nil {
		return err
	}
	if err := importBackends(tx, cfg.Daemon.AIBackends); err != nil {
		return err
	}
	if err := importSkills(tx, cfg.Skills); err != nil {
		return err
	}
	if err := importAgents(tx, cfg.Agents); err != nil {
		return err
	}
	if err := importRepos(tx, cfg.Repos); err != nil {
		return err
	}
	return tx.Commit()
}

// daemonRecord is a JSON-serializable view of DaemonConfig that excludes
// resolved secret values (only the env-var name fields are kept).
type daemonRecord struct {
	Log       config.LogConfig       `json:"log"`
	HTTP      httpRecord             `json:"http"`
	Processor config.ProcessorConfig `json:"processor"`
	Proxy     proxyRecord            `json:"proxy"`
}

// httpRecord mirrors HTTPConfig but omits resolved secret fields.
type httpRecord struct {
	ListenAddr             string `json:"listen_addr"`
	StatusPath             string `json:"status_path"`
	WebhookPath            string `json:"webhook_path"`
	WebhookSecretEnv       string `json:"webhook_secret_env"`
	ReadTimeoutSeconds     int    `json:"read_timeout_seconds"`
	WriteTimeoutSeconds    int    `json:"write_timeout_seconds"`
	IdleTimeoutSeconds     int    `json:"idle_timeout_seconds"`
	MaxBodyBytes           int64  `json:"max_body_bytes"`
	DeliveryTTLSeconds     int    `json:"delivery_ttl_seconds"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
}

// proxyRecord mirrors ProxyConfig but omits the resolved APIKey.
type proxyRecord struct {
	Enabled  bool                `json:"enabled"`
	Path     string              `json:"path"`
	Upstream proxyUpstreamRecord `json:"upstream"`
}

type proxyUpstreamRecord struct {
	URL            string         `json:"url"`
	Model          string         `json:"model"`
	APIKeyEnv      string         `json:"api_key_env"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	ExtraBody      map[string]any `json:"extra_body,omitempty"`
}

func importDaemon(tx *sql.Tx, d config.DaemonConfig) error {
	rec := daemonRecord{
		Log: d.Log,
		HTTP: httpRecord{
			ListenAddr:       d.HTTP.ListenAddr,
			StatusPath:       d.HTTP.StatusPath,
			WebhookPath:      d.HTTP.WebhookPath,
			WebhookSecretEnv: d.HTTP.WebhookSecretEnv,

			ReadTimeoutSeconds:     d.HTTP.ReadTimeoutSeconds,
			WriteTimeoutSeconds:    d.HTTP.WriteTimeoutSeconds,
			IdleTimeoutSeconds:     d.HTTP.IdleTimeoutSeconds,
			MaxBodyBytes:           d.HTTP.MaxBodyBytes,
			DeliveryTTLSeconds:     d.HTTP.DeliveryTTLSeconds,
			ShutdownTimeoutSeconds: d.HTTP.ShutdownTimeoutSeconds,
		},
		Processor: d.Processor,
		Proxy: proxyRecord{
			Enabled: d.Proxy.Enabled,
			Path:    d.Proxy.Path,
			Upstream: proxyUpstreamRecord{
				URL:            d.Proxy.Upstream.URL,
				Model:          d.Proxy.Upstream.Model,
				APIKeyEnv:      d.Proxy.Upstream.APIKeyEnv,
				TimeoutSeconds: d.Proxy.Upstream.TimeoutSeconds,
				ExtraBody:      d.Proxy.Upstream.ExtraBody,
			},
		},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store import: marshal daemon config: %w", err)
	}
	if _, err := tx.Exec(
		"INSERT OR REPLACE INTO config(key,value) VALUES('daemon',?)", string(data),
	); err != nil {
		return fmt.Errorf("store import: upsert daemon config: %w", err)
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
			  (name,command,version,models,healthy,health_detail,local_model_url,timeout_seconds,max_prompt_chars,redaction_salt_env)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			name, b.Command,
			b.Version, string(models), healthy, b.HealthDetail, b.LocalModelURL,
			b.TimeoutSeconds, b.MaxPromptChars, b.RedactionSaltEnv,
		); err != nil {
			return fmt.Errorf("store import: upsert backend %s: %w", name, err)
		}
	}
	return nil
}

func importSkills(tx *sql.Tx, skills map[string]fleet.Skill) error {
	for name, s := range skills {
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO skills(name,prompt) VALUES(?,?)",
			name, s.Prompt,
		); err != nil {
			return fmt.Errorf("store import: upsert skill %s: %w", name, err)
		}
	}
	return nil
}

func importAgents(tx *sql.Tx, agents []fleet.Agent) error {
	for _, a := range agents {
		skills, err := json.Marshal(a.Skills)
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
			INSERT OR REPLACE INTO agents
			  (name,backend,model,skills,prompt,allow_prs,allow_dispatch,can_dispatch,description,allow_memory)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			a.Name, a.Backend, a.Model, string(skills), a.Prompt,
			allowPRs, allowDispatch, string(canDispatch), a.Description, allowMemory,
		); err != nil {
			return fmt.Errorf("store import: upsert agent %s: %w", a.Name, err)
		}
	}
	return nil
}

func importRepos(tx *sql.Tx, repos []fleet.Repo) error {
	for _, r := range repos {
		enabled := boolToInt(r.Enabled)
		if _, err := tx.Exec(
			"INSERT OR REPLACE INTO repos(name,enabled) VALUES(?,?)",
			r.Name, enabled,
		); err != nil {
			return fmt.Errorf("store import: upsert repo %s: %w", r.Name, err)
		}
		// Delete and re-insert bindings so that re-importing the same YAML
		// does not accumulate duplicate rows. A repo's binding list is treated
		// as a whole (replace-all semantics): remove what was there, write
		// what the new config says.
		if _, err := tx.Exec("DELETE FROM bindings WHERE repo=?", r.Name); err != nil {
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
				INSERT INTO bindings(repo,agent,labels,events,cron,enabled)
				VALUES (?,?,?,?,?,?)`,
				r.Name, b.Agent, string(labels), string(events), b.Cron, bindingEnabled,
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

	if err := loadDaemon(db, cfg); err != nil {
		return nil, err
	}
	if err := loadBackends(db, cfg); err != nil {
		return nil, err
	}
	if err := loadSkills(db, cfg); err != nil {
		return nil, err
	}
	if err := loadAgents(db, cfg); err != nil {
		return nil, err
	}
	if err := loadRepos(db, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadDaemon(db *sql.DB, cfg *config.Config) error {
	var value string
	err := db.QueryRow("SELECT value FROM config WHERE key='daemon'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store load: daemon config not found in database (did you run --import?)")
	}
	if err != nil {
		return fmt.Errorf("store load: query daemon config: %w", err)
	}

	var rec daemonRecord
	if err := json.Unmarshal([]byte(value), &rec); err != nil {
		return fmt.Errorf("store load: parse daemon config: %w", err)
	}

	cfg.Daemon.Log = rec.Log
	cfg.Daemon.HTTP = config.HTTPConfig{
		ListenAddr:       rec.HTTP.ListenAddr,
		StatusPath:       rec.HTTP.StatusPath,
		WebhookPath:      rec.HTTP.WebhookPath,
		WebhookSecretEnv: rec.HTTP.WebhookSecretEnv,

		ReadTimeoutSeconds:     rec.HTTP.ReadTimeoutSeconds,
		WriteTimeoutSeconds:    rec.HTTP.WriteTimeoutSeconds,
		IdleTimeoutSeconds:     rec.HTTP.IdleTimeoutSeconds,
		MaxBodyBytes:           rec.HTTP.MaxBodyBytes,
		DeliveryTTLSeconds:     rec.HTTP.DeliveryTTLSeconds,
		ShutdownTimeoutSeconds: rec.HTTP.ShutdownTimeoutSeconds,
	}
	cfg.Daemon.Processor = rec.Processor
	cfg.Daemon.Proxy = config.ProxyConfig{
		Enabled: rec.Proxy.Enabled,
		Path:    rec.Proxy.Path,
		Upstream: config.ProxyUpstreamConfig{
			URL:            rec.Proxy.Upstream.URL,
			Model:          rec.Proxy.Upstream.Model,
			APIKeyEnv:      rec.Proxy.Upstream.APIKeyEnv,
			TimeoutSeconds: rec.Proxy.Upstream.TimeoutSeconds,
			ExtraBody:      rec.Proxy.Upstream.ExtraBody,
		},
	}
	return nil
}

func loadBackends(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT name,command,version,models,healthy,health_detail,local_model_url,timeout_seconds,max_prompt_chars,redaction_salt_env FROM backends")
	if err != nil {
		return fmt.Errorf("store load: query backends: %w", err)
	}
	defer rows.Close()

	backends := make(map[string]fleet.Backend)
	for rows.Next() {
		var name, command, version, modelsJSON, healthDetail, localModelURL, saltEnv string
		var timeout, maxChars, healthy int
		if err := rows.Scan(&name, &command, &version, &modelsJSON, &healthy, &healthDetail, &localModelURL, &timeout, &maxChars, &saltEnv); err != nil {
			return fmt.Errorf("store load: scan backend: %w", err)
		}
		var models []string
		if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
			return fmt.Errorf("store load: parse backend %s models: %w", name, err)
		}
		backends[name] = fleet.Backend{
			Command:          command,
			Version:          version,
			Models:           models,
			Healthy:          intToBool(healthy),
			HealthDetail:     healthDetail,
			LocalModelURL:    localModelURL,
			TimeoutSeconds:   timeout,
			MaxPromptChars:   maxChars,
			RedactionSaltEnv: saltEnv,
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate backends: %w", err)
	}
	cfg.Daemon.AIBackends = backends
	return nil
}

func loadSkills(db querier, cfg *config.Config) error {
	rows, err := db.Query("SELECT name,prompt FROM skills")
	if err != nil {
		return fmt.Errorf("store load: query skills: %w", err)
	}
	defer rows.Close()

	skills := make(map[string]fleet.Skill)
	for rows.Next() {
		var name, prompt string
		if err := rows.Scan(&name, &prompt); err != nil {
			return fmt.Errorf("store load: scan skill: %w", err)
		}
		skills[name] = fleet.Skill{Prompt: prompt}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate skills: %w", err)
	}
	cfg.Skills = skills
	return nil
}

func loadAgents(db querier, cfg *config.Config) error {
	rows, err := db.Query(`
		SELECT name,backend,model,skills,prompt,allow_prs,allow_dispatch,can_dispatch,description,allow_memory
		FROM agents ORDER BY name`)
	if err != nil {
		return fmt.Errorf("store load: query agents: %w", err)
	}
	defer rows.Close()

	var agents []fleet.Agent
	for rows.Next() {
		var name, backend, model, skillsJSON, prompt, canDispatchJSON, description string
		var allowPRs, allowDispatch, allowMemory int
		if err := rows.Scan(
			&name, &backend, &model, &skillsJSON, &prompt,
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
		agents = append(agents, fleet.Agent{
			Name:          name,
			Backend:       backend,
			Model:         model,
			Skills:        skills,
			Prompt:        prompt,
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
	rows, err := db.Query("SELECT name,enabled FROM repos ORDER BY name")
	if err != nil {
		return fmt.Errorf("store load: query repos: %w", err)
	}
	defer rows.Close()

	var repos []fleet.Repo
	for rows.Next() {
		var name string
		var enabled int
		if err := rows.Scan(&name, &enabled); err != nil {
			return fmt.Errorf("store load: scan repo: %w", err)
		}
		repos = append(repos, fleet.Repo{Name: name, Enabled: intToBool(enabled)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store load: iterate repos: %w", err)
	}

	// Load bindings for each repo.
	for i := range repos {
		bindings, err := loadBindingsForRepo(db, repos[i].Name)
		if err != nil {
			return err
		}
		repos[i].Use = bindings
	}
	cfg.Repos = repos
	return nil
}

func loadBindingsForRepo(db querier, repo string) ([]fleet.Binding, error) {
	rows, err := db.Query(
		"SELECT id,agent,labels,events,cron,enabled FROM bindings WHERE repo=? ORDER BY id", repo,
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
// last-updated timestamp for (agent, repo). If no row exists, it returns
// ("", false, time.Time{}, nil). An empty content string with found=true
// means the agent intentionally cleared its memory.
func ReadMemory(db *sql.DB, agent, repo string) (string, bool, time.Time, error) {
	var content, updatedAt string
	err := db.QueryRow(
		"SELECT content, updated_at FROM memory WHERE agent=? AND repo=?", agent, repo,
	).Scan(&content, &updatedAt)
	if err == sql.ErrNoRows {
		return "", false, time.Time{}, nil
	}
	if err != nil {
		return "", false, time.Time{}, fmt.Errorf("store: read memory %s/%s: %w", agent, repo, err)
	}
	// The modernc.org/sqlite driver returns TIMESTAMP columns as RFC3339 strings
	// (e.g. "2026-04-21T10:30:00Z"). Parse with time.RFC3339 and fall back to
	// the bare "YYYY-MM-DD HH:MM:SS" SQLite text format as a safety net.
	t, parseErr := time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
	}
	return content, true, t.UTC(), nil
}

// WriteMemory upserts the memory string for (agent, repo), setting updated_at
// to the current UTC timestamp.
func WriteMemory(db *sql.DB, agent, repo, content string) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO memory(agent,repo,content,updated_at) VALUES(?,?,?,datetime('now'))`,
		agent, repo, content,
	)
	if err != nil {
		return fmt.Errorf("store: write memory %s/%s: %w", agent, repo, err)
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
