// Package repos implements the repos and per-binding HTTP CRUD surface.
// Handlers and the methods exposed for the MCP fleet-management tools live
// together in this package so that the wire format, the validation gate,
// and the storage path stay in sync.
//
// The handler reads from SQLite on every request and writes through the
// store package's per-call transactions. The static MaxBodyBytes limit is
// captured at construction since it never mutates via CRUD.
package repos

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

// Handler implements the /repos and /repos/{owner}/{repo}/bindings HTTP
// surface plus the methods exposed for the MCP repo and binding tools.
type Handler struct {
	store        *store.Store
	maxBodyBytes int64
	logger       zerolog.Logger
}

// New constructs a Handler. store is the data-access facade;
// maxBodyBytes caps incoming request bodies for write endpoints.
func New(st *store.Store, maxBodyBytes int64, logger zerolog.Logger) *Handler {
	return &Handler{
		store:        st,
		maxBodyBytes: maxBodyBytes,
		logger:       logger.With().Str("component", "server_repos").Logger(),
	}
}

// RegisterRoutes mounts the repo + binding endpoints on r. withTimeout wraps
// each handler in an http.TimeoutHandler matching the daemon's
// HTTP write-timeout setting.
func (h *Handler) RegisterRoutes(r *mux.Router, withTimeout func(http.Handler) http.Handler) {
	r.Handle("/repos", withTimeout(http.HandlerFunc(h.handleReposList))).Methods(http.MethodGet)
	r.Handle("/repos", withTimeout(http.HandlerFunc(h.handleRepoCreate))).Methods(http.MethodPost)
	r.Handle("/repos/{owner}/{repo}", withTimeout(http.HandlerFunc(h.handleRepoGet))).Methods(http.MethodGet)
	r.Handle("/repos/{owner}/{repo}", withTimeout(http.HandlerFunc(h.handleRepoPatch))).Methods(http.MethodPatch)
	r.Handle("/repos/{owner}/{repo}", withTimeout(http.HandlerFunc(h.handleRepoDelete))).Methods(http.MethodDelete)
	r.Handle("/repos/{owner}/{repo}/bindings", withTimeout(http.HandlerFunc(h.handleCreateBinding))).Methods(http.MethodPost)
	r.Handle("/repos/{owner}/{repo}/bindings/{id}", withTimeout(http.HandlerFunc(h.handleGetBinding))).Methods(http.MethodGet)
	r.Handle("/repos/{owner}/{repo}/bindings/{id}", withTimeout(http.HandlerFunc(h.handleUpdateBinding))).Methods(http.MethodPatch)
	r.Handle("/repos/{owner}/{repo}/bindings/{id}", withTimeout(http.HandlerFunc(h.handleDeleteBinding))).Methods(http.MethodDelete)
}

// ── Wire types ──────────────────────────────────────────────────────────────────────

// storeBindingJSON is the wire shape for one binding inside a repo (atomic
// per-binding routes use the same shape).
type storeBindingJSON struct {
	ID      int64    `json:"id,omitempty"`
	Agent   string   `json:"agent"`
	Labels  []string `json:"labels,omitempty"`
	Events  []string `json:"events,omitempty"`
	Cron    string   `json:"cron,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
}

func bindingToStoreJSON(b fleet.Binding) storeBindingJSON {
	enabled := b.IsEnabled()
	return storeBindingJSON{
		ID:      b.ID,
		Agent:   b.Agent,
		Labels:  b.Labels,
		Events:  b.Events,
		Cron:    b.Cron,
		Enabled: &enabled,
	}
}

func (j storeBindingJSON) toConfig() fleet.Binding {
	return fleet.Binding{
		ID:      j.ID,
		Agent:   j.Agent,
		Labels:  j.Labels,
		Events:  j.Events,
		Cron:    j.Cron,
		Enabled: j.Enabled,
	}
}

// storeRepoJSON is the wire shape for POST/GET /repos. POST replaces all
// bindings on the repo.
type storeRepoJSON struct {
	WorkspaceID string             `json:"workspace_id,omitempty"`
	Name        string             `json:"name"`
	Enabled     bool               `json:"enabled"`
	Bindings    []storeBindingJSON `json:"bindings"`
}

func repoToStoreJSON(r fleet.Repo) storeRepoJSON {
	bindings := make([]storeBindingJSON, len(r.Use))
	for i, b := range r.Use {
		bindings[i] = bindingToStoreJSON(b)
	}
	return storeRepoJSON{WorkspaceID: r.WorkspaceID, Name: r.Name, Enabled: r.Enabled, Bindings: bindings}
}

func (j storeRepoJSON) toConfig() fleet.Repo {
	use := make([]fleet.Binding, len(j.Bindings))
	for i, b := range j.Bindings {
		use[i] = b.toConfig()
	}
	return fleet.Repo{WorkspaceID: j.WorkspaceID, Name: j.Name, Enabled: j.Enabled, Use: use}
}

// repoRuntimeSettingsJSON is the wire shape for PATCH /repos/{owner}/{repo}.
// Only the enabled flag is currently mutable via PATCH; name changes require
// a delete+create since the name is the primary key.
type repoRuntimeSettingsJSON struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────────────

func (h *Handler) handleReposList(w http.ResponseWriter, r *http.Request) {
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	repos, err := h.store.ReadRepos()
	if err != nil {
		http.Error(w, fmt.Sprintf("read repos: %v", err), http.StatusInternalServerError)
		return
	}
	out := make([]storeRepoJSON, 0, len(repos))
	for _, r := range repos {
		if fleet.NormalizeWorkspaceID(r.WorkspaceID) != workspaceID {
			continue
		}
		out = append(out, repoToStoreJSON(r))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleRepoCreate(w http.ResponseWriter, r *http.Request) {
	var req storeRepoJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if req.WorkspaceID == "" {
		req.WorkspaceID = fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	}
	canonical, err := h.UpsertRepo(req.toConfig())
	if err != nil {
		h.writeErr(w, err, "repo upsert or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, repoToStoreJSON(canonical))
}

func repoNameFromRequest(r *http.Request) string {
	vars := mux.Vars(r)
	return fleet.NormalizeRepoName(vars["owner"]) + "/" + fleet.NormalizeRepoName(vars["repo"])
}

func (h *Handler) handleRepoGet(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromRequest(r)
	workspaceID := fleet.NormalizeWorkspaceID(r.URL.Query().Get("workspace"))
	repos, err := h.store.ReadRepos()
	if err != nil {
		http.Error(w, fmt.Sprintf("read repos: %v", err), http.StatusInternalServerError)
		return
	}
	for _, repo := range repos {
		if repo.Name == repoName && fleet.NormalizeWorkspaceID(repo.WorkspaceID) == workspaceID {
			writeJSON(w, http.StatusOK, repoToStoreJSON(repo))
			return
		}
	}
	http.NotFound(w, r)
}

func (h *Handler) handleRepoPatch(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromRequest(r)
	var req repoRuntimeSettingsJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	if req.Enabled == nil {
		http.Error(w, "at least one field is required", http.StatusBadRequest)
		return
	}
	repo, err := h.PatchRepo(repoName, *req.Enabled)
	if err != nil {
		h.writeErr(w, err, "repo patch or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, repoToStoreJSON(repo))
}

func (h *Handler) handleRepoDelete(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromRequest(r)
	if err := h.DeleteRepo(repoName); err != nil {
		h.writeErr(w, err, "repo delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	var req storeBindingJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	// Ignore any ID the client sends, the store picks it.
	req.ID = 0
	b, err := h.CreateBinding(repoName, req.toConfig())
	if err != nil {
		h.writeErr(w, err, "binding create or cron reload")
		return
	}
	writeJSON(w, http.StatusCreated, bindingToStoreJSON(b))
}

func (h *Handler) handleGetBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	owner, b, found, err := h.store.ReadBinding(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("read binding: %v", err), http.StatusInternalServerError)
		return
	}
	if !found || owner != repoName {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, bindingToStoreJSON(b))
}

func (h *Handler) handleUpdateBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	var req storeBindingJSON
	if !decodeBody(w, r, h.maxBodyBytes, &req) {
		return
	}
	b, err := h.UpdateBinding(repoName, id, req.toConfig())
	if err != nil {
		h.writeErr(w, err, "binding update or cron reload")
		return
	}
	writeJSON(w, http.StatusOK, bindingToStoreJSON(b))
}

func (h *Handler) handleDeleteBinding(w http.ResponseWriter, r *http.Request) {
	repoName := repoNameFromVars(r)
	id, ok := bindingIDFromVars(w, r)
	if !ok {
		return
	}
	if err := h.DeleteBinding(repoName, id); err != nil {
		h.writeErr(w, err, "binding delete or cron reload")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Methods exposed for non-HTTP callers (MCP tools) ─────────────────────────────────────────────

// UpsertRepo writes a single repo definition (and its bindings) into the store
// and reloads the cron scheduler. Returns the canonical (normalized) form so
// callers can surface the exact shape REST clients see in the POST /repos
// response, lowercase repo name, lowercased binding agents, trimmed cron, and
// lowercased events.
//
// Empty/whitespace names are rejected as *store.ErrValidation so callers can
// map them to HTTP 400 / MCP user errors.
func (h *Handler) UpsertRepo(r fleet.Repo) (fleet.Repo, error) {
	if err := fleet.ValidateRepoName(r.Name); err != nil {
		return fleet.Repo{}, &store.ErrValidation{Msg: err.Error()}
	}
	if err := h.store.UpsertRepo(r); err != nil {
		return fleet.Repo{}, err
	}
	fleet.NormalizeRepo(&r)
	return r, nil
}

// PatchRepo updates the enabled flag on an existing repo without touching its
// bindings. Returns the canonical Repo (with current bindings) so callers can
// refresh their view. *store.ErrNotFound when the repo does not exist.
func (h *Handler) PatchRepo(repoName string, enabled bool) (fleet.Repo, error) {
	// Load the repo (and its bindings) so we can return it intact.
	repos, err := h.store.ReadRepos()
	if err != nil {
		return fleet.Repo{}, err
	}
	idx := slices.IndexFunc(repos, func(r fleet.Repo) bool { return r.Name == repoName })
	if idx == -1 {
		return fleet.Repo{}, &store.ErrNotFound{Msg: fmt.Sprintf("repo %q not found", repoName)}
	}
	existing := &repos[idx]
	if existing.Enabled == enabled {
		return *existing, nil
	}
	// Flip the enabled flag via a direct UPDATE so we don't re-run the
	// delete+insert cycle that UpsertRepo performs on bindings.
	if err := h.store.EnableRepo(repoName, enabled); err != nil {
		return fleet.Repo{}, fmt.Errorf("patch repo %s: %w", repoName, err)
	}
	existing.Enabled = enabled
	return *existing, nil
}

// DeleteRepo removes the named repo (and cascades its bindings). The
// scheduler picks up the change on its next reconcile tick.
func (h *Handler) DeleteRepo(name string) error {
	return h.store.DeleteRepo(name)
}

// CreateBinding persists a new binding on repoName.
func (h *Handler) CreateBinding(repoName string, b fleet.Binding) (fleet.Binding, error) {
	_, persisted, err := h.store.CreateBinding(repoName, b)
	if err != nil {
		return fleet.Binding{}, err
	}
	return persisted, nil
}

// UpdateBinding verifies the id belongs to repoName and replaces the row.
func (h *Handler) UpdateBinding(repoName string, id int64, b fleet.Binding) (fleet.Binding, error) {
	existingRepo, _, found, err := h.store.ReadBinding(id)
	if err != nil {
		return fleet.Binding{}, err
	}
	if !found || existingRepo != repoName {
		return fleet.Binding{}, &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	return h.store.UpdateBinding(id, b)
}

// ReadBinding fetches one binding by ID, verifying it belongs to repoName.
func (h *Handler) ReadBinding(repoName string, id int64) (fleet.Binding, error) {
	existingRepo, b, found, err := h.store.ReadBinding(id)
	if err != nil {
		return fleet.Binding{}, err
	}
	if !found || existingRepo != repoName {
		return fleet.Binding{}, &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	return b, nil
}

// DeleteBinding verifies the id belongs to repoName and deletes it.
func (h *Handler) DeleteBinding(repoName string, id int64) error {
	existingRepo, _, found, err := h.store.ReadBinding(id)
	if err != nil {
		return err
	}
	if !found || existingRepo != repoName {
		return &store.ErrNotFound{Msg: fmt.Sprintf("binding id=%d not found for repo %q", id, repoName)}
	}
	return h.store.DeleteBinding(id)
}

// ── Helpers ────────────────────────────────────────────────────────────────────────────

// repoNameFromVars reconstructs the normalized owner/repo path parameter.
func repoNameFromVars(r *http.Request) string {
	vars := mux.Vars(r)
	return fleet.NormalizeRepoName(vars["owner"]) + "/" + fleet.NormalizeRepoName(vars["repo"])
}

// bindingIDFromVars parses the {id} path parameter. On error it writes a 400
// response and returns (0, false).
func bindingIDFromVars(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(mux.Vars(r)["id"])
	if raw == "" {
		http.Error(w, "binding id is required", http.StatusBadRequest)
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, fmt.Sprintf("invalid binding id %q", raw), http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// writeErr maps a store error to an HTTP response and a structured log entry.
// op identifies the failing operation (e.g. "repo upsert or cron reload") and
// appears in both the log line and the HTTP error body so callers and
// operators see the same context.
func (h *Handler) writeErr(w http.ResponseWriter, err error, op string) {
	h.logger.Error().Err(err).Msgf("store crud: %s failed", op)
	http.Error(w, fmt.Sprintf("%s: %v", op, err), storeErrStatus(err))
}

// storeErrStatus maps a store error to an HTTP status. Validation and
// not-found errors surface as 400 and 404 respectively; conflict errors as
// 409. Everything else falls back to 500 so unexpected failures are loud.
func storeErrStatus(err error) int {
	var v *store.ErrValidation
	if errors.As(err, &v) {
		return http.StatusBadRequest
	}
	var n *store.ErrNotFound
	if errors.As(err, &n) {
		return http.StatusNotFound
	}
	var c *store.ErrConflict
	if errors.As(err, &c) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// decodeBody reads and decodes a JSON body up to limit bytes. On error it
// writes the response and returns false; callers must not write further.
// Bodies larger than limit surface as 413; malformed JSON as 400.
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

// writeJSON encodes v as JSON and writes it with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
