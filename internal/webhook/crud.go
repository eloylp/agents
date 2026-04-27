package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func nilSafeStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody reads the full request body (enforcing the byte limit via
// http.MaxBytesReader so that trailing garbage beyond a valid JSON value is
// also accounted for), then JSON-unmarshals it into out. It returns false and
// writes an appropriate HTTP error on any failure.
func decodeBody[T any](w http.ResponseWriter, r *http.Request, limit int64, out *T) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

// storeErrStatus maps store mutation errors to the appropriate HTTP status
// code. ErrValidation (bad field values) → 400, ErrConflict (invariant
// violations, referenced-by failures) → 409, ErrNotFound → 404,
// anything else → 500.
func storeErrStatus(err error) int {
	var valErr *store.ErrValidation
	if errors.As(err, &valErr) {
		return http.StatusBadRequest
	}
	var conflictErr *store.ErrConflict
	if errors.As(err, &conflictErr) {
		return http.StatusConflict
	}
	var notFoundErr *store.ErrNotFound
	if errors.As(err, &notFoundErr) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// storeWriteErr maps an error from a store write operation to an HTTP response
// and a structured log entry. op identifies the failing operation (e.g.
// "agent upsert or cron reload") and appears in both the log line and the
// HTTP error body so callers and operators see the same context.
func (s *Server) storeWriteErr(w http.ResponseWriter, err error, op string) {
	s.logger.Error().Err(err).Msgf("store crud: %s failed", op)
	http.Error(w, fmt.Sprintf("%s: %v", op, err), storeErrStatus(err))
}

// reloadCron re-reads the full config from the DB as a consistent snapshot
// and calls Reload on the attached CronReloader (if any). All four entity
// types are read within a single transaction so a concurrent /api/store write
// cannot produce a mixed-epoch snapshot.
//
// MUST be called with s.storeMu held so that no other write can commit and
// re-read a newer snapshot between the point this read begins and the point
// Reload applies the result. Without the lock the "DB commit + snapshot read +
// Reload" sequence is not monotonic: a slow caller can overwrite a newer
// in-memory state that a concurrent faster caller already applied.
func (s *Server) reloadCron() error {
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return fmt.Errorf("read config snapshot for cron reload: %w", err)
	}

	// Reload the scheduler/engine first. If Reload fails we must not update
	// the server's routing config — doing so would leave the daemon split across
	// two config epochs (server on the new snapshot, scheduler/engine on the
	// old one) until the next successful reload or restart.
	if s.cronReloader != nil {
		if err := s.cronReloader.Reload(repos, agents, skills, backends); err != nil {
			return err
		}
	}

	// Reload succeeded: update the server's in-memory routing config so that
	// webhook event handlers (/webhooks/github, /agents/run) and read APIs
	// (/api/agents, /api/config) reflect the post-write state immediately
	// without a restart. Copy-on-write: build a new config value from the
	// current snapshot, replacing only the four CRUD-mutable fields.
	// Daemon-level config (HTTP, proxy, log) is never changed by CRUD writes
	// and is preserved unchanged.
	s.cfgMu.Lock()
	newCfg := *s.cfg
	newCfg.Repos = repos
	newCfg.Agents = agents
	newCfg.Skills = skills
	newCfg.Daemon.AIBackends = backends
	s.cfg = &newCfg
	s.cfgMu.Unlock()
	s.refreshOrphanedAgents(&newCfg)

	return nil
}

// ── /api/store/export and /api/store/import ───────────────────────────────────

// exportYAML is the wire shape for YAML export/import. It captures only the
// four CRUD-mutable sections; daemon-level config (HTTP, log, proxy) is
// intentionally excluded — it is not managed by the write API.
type exportYAML struct {
	Skills map[string]fleet.Skill `yaml:"skills,omitempty"`
	Agents []fleet.Agent          `yaml:"agents,omitempty"`
	Repos  []fleet.Repo           `yaml:"repos,omitempty"`
	Daemon *exportDaemonYAML          `yaml:"daemon,omitempty"`
}

type exportDaemonYAML struct {
	AIBackends map[string]fleet.Backend `yaml:"ai_backends,omitempty"`
}

// handleStoreExport serves GET /api/store/export — returns a config.yaml
// fragment covering the four CRUD-mutable sections (skills, agents, repos,
// daemon.ai_backends). The API key is required because backends may contain
// secret env values.
func (s *Server) handleStoreExport(w http.ResponseWriter, _ *http.Request) {
	b, err := s.ExportYAML()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="config-export.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// ExportYAML returns the CRUD-mutable sections of the store as a YAML fragment
// matching the GET /export response body. Exposed so non-HTTP surfaces (e.g.
// the MCP export_config tool) can serve the same payload the REST endpoint
// returns without going through the router.
func (s *Server) ExportYAML() ([]byte, error) {
	agents, repos, skills, backends, err := store.ReadSnapshot(s.db)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	out := exportYAML{
		Skills: skills,
		Agents: agents,
		Repos:  repos,
	}
	if len(backends) > 0 {
		out.Daemon = &exportDaemonYAML{AIBackends: backends}
	}
	b, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	return b, nil
}

// handleStoreImport serves POST /api/store/import — accepts a YAML body in
// the same format as handleStoreExport and upserts all entities into the DB.
// On success it returns 200 with a JSON summary of imported counts.
func (s *Server) handleStoreImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.loadCfg().Daemon.HTTP.MaxBodyBytes*10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("read request: %v", err), http.StatusBadRequest)
		return
	}
	counts, err := s.ImportYAML(body, r.URL.Query().Get("mode"))
	if err != nil {
		http.Error(w, err.Error(), storeErrStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

// ImportYAML parses a YAML payload in handleStoreExport's format and writes it
// to the store. mode controls upsert semantics: empty or "merge" preserves
// existing records, "replace" prunes anything not present in the payload.
//
// On success it returns the per-section counts that handleStoreImport ships in
// its JSON response. Validation failures (bad mode, malformed YAML, store-level
// invariants) are returned as *store.ErrValidation so callers can map them to
// HTTP 400 / MCP user errors via storeErrStatus.
//
// Exposed so non-HTTP surfaces (e.g. the MCP import_config tool) can run the
// same import path as POST /import without going through the router.
func (s *Server) ImportYAML(body []byte, mode string) (map[string]int, error) {
	if mode != "" && mode != "merge" && mode != "replace" {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("invalid mode %q: must be empty, \"merge\", or \"replace\"", mode)}
	}
	var payload exportYAML
	if err := yaml.Unmarshal(body, &payload); err != nil {
		return nil, &store.ErrValidation{Msg: fmt.Sprintf("parse yaml: %v", err)}
	}

	backends := map[string]fleet.Backend{}
	if payload.Daemon != nil {
		backends = payload.Daemon.AIBackends
	}

	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	var importErr error
	if mode == "replace" {
		importErr = store.ReplaceAll(s.db, payload.Agents, payload.Repos, payload.Skills, backends)
	} else {
		importErr = store.ImportAll(s.db, payload.Agents, payload.Repos, payload.Skills, backends)
	}
	if importErr != nil {
		return nil, fmt.Errorf("import: %w", importErr)
	}
	if err := s.reloadCron(); err != nil {
		s.logger.Error().Err(err).Msg("store import: cron reload failed")
		return nil, fmt.Errorf("cron reload: %w", err)
	}

	return map[string]int{
		"agents":   len(payload.Agents),
		"skills":   len(payload.Skills),
		"repos":    len(payload.Repos),
		"backends": len(backends),
	}, nil
}
