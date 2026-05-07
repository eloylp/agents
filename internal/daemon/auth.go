package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/eloylp/agents/internal/store"
)

const (
	sessionCookieName = "agents_session"
	sessionTTL        = 7 * 24 * time.Hour
)

type authContextKey struct{}

type authStatusResponse struct {
	BootstrapRequired bool        `json:"bootstrap_required"`
	Authenticated     bool        `json:"authenticated"`
	User              *store.User `json:"user,omitempty"`
}

type authCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type createTokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type createTokenResponse struct {
	store.AuthToken
	Token string `json:"token"`
}

func (d *Daemon) registerAuthRoutes(router *mux.Router, withTimeout func(http.Handler) http.Handler) {
	router.Handle("/auth/status", withTimeout(http.HandlerFunc(d.handleAuthStatus))).Methods(http.MethodGet)
	router.Handle("/auth/bootstrap", withTimeout(http.HandlerFunc(d.handleAuthBootstrap))).Methods(http.MethodPost)
	router.Handle("/auth/login", withTimeout(http.HandlerFunc(d.handleAuthLogin))).Methods(http.MethodPost)
	router.Handle("/auth/logout", withTimeout(http.HandlerFunc(d.handleAuthLogout))).Methods(http.MethodPost)
	router.Handle("/auth/me", withTimeout(http.HandlerFunc(d.handleAuthMe))).Methods(http.MethodGet)
	router.Handle("/auth/users", withTimeout(http.HandlerFunc(d.handleAuthUsersList))).Methods(http.MethodGet)
	router.Handle("/auth/users", withTimeout(http.HandlerFunc(d.handleAuthUsersCreate))).Methods(http.MethodPost)
	router.Handle("/auth/tokens", withTimeout(http.HandlerFunc(d.handleAuthTokensList))).Methods(http.MethodGet)
	router.Handle("/auth/tokens", withTimeout(http.HandlerFunc(d.handleAuthTokensCreate))).Methods(http.MethodPost)
	router.Handle("/auth/tokens/{id}", withTimeout(http.HandlerFunc(d.handleAuthTokenRevoke))).Methods(http.MethodDelete)
}

func (d *Daemon) withBearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		identity, ok := d.authenticateRequest(r)
		if ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, identity)))
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="agents"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (d *Daemon) isPublicRoute(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodGet && path == d.daemonCfg.HTTP.StatusPath {
		return true
	}
	if path == d.daemonCfg.HTTP.WebhookPath {
		return true
	}
	if path == "/auth/status" || path == "/auth/login" || path == "/auth/bootstrap" {
		return true
	}
	if path == "/" || path == "/ui" || path == "/ui/" || strings.HasPrefix(path, "/ui/") {
		return true
	}
	if d.proxy != nil && (path == d.daemonCfg.Proxy.Path || path == "/v1/models") && isLoopbackRequest(r) {
		return true
	}
	return false
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (d *Daemon) authenticateRequest(r *http.Request) (store.AuthIdentity, bool) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if identity, err := d.store.AuthenticateToken(r.Context(), cookie.Value, store.AuthTokenKindSession); err == nil {
			return identity, true
		}
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if ok && strings.EqualFold(scheme, "Bearer") {
		if identity, err := d.store.AuthenticateToken(r.Context(), strings.TrimSpace(token), store.AuthTokenKindAPI); err == nil {
			return identity, true
		}
	}
	return store.AuthIdentity{}, false
}

func (d *Daemon) currentIdentity(r *http.Request) (store.AuthIdentity, bool) {
	if identity, ok := r.Context().Value(authContextKey{}).(store.AuthIdentity); ok {
		return identity, true
	}
	return d.authenticateRequest(r)
}

func (d *Daemon) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	count, err := d.store.UserCount(r.Context())
	if err != nil {
		d.logger.Error().Err(err).Msg("auth status: count users")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := authStatusResponse{
		BootstrapRequired: count == 0,
	}
	if identity, ok := d.currentIdentity(r); ok {
		resp.Authenticated = true
		resp.User = &identity.User
	}
	writeJSON(w, resp, http.StatusOK)
}

func (d *Daemon) handleAuthBootstrap(w http.ResponseWriter, r *http.Request) {
	count, err := d.store.UserCount(r.Context())
	if err != nil {
		d.logger.Error().Err(err).Msg("auth bootstrap: count users")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if count != 0 {
		http.Error(w, "bootstrap closed", http.StatusConflict)
		return
	}
	var req authCredentialsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, d.daemonCfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	created, err := d.store.BootstrapUser(r.Context(), req.Username, req.Password, sessionTTL)
	if errors.Is(err, store.ErrAuthInvalid) {
		http.Error(w, "invalid credentials", http.StatusBadRequest)
		return
	}
	if errors.Is(err, store.ErrBootstrapClosed) {
		http.Error(w, "bootstrap closed", http.StatusConflict)
		return
	}
	if err != nil {
		d.logger.Error().Err(err).Msg("auth bootstrap")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, created.Token, created.ExpiresAt)
	writeJSON(w, map[string]any{"ok": true}, http.StatusCreated)
}

func (d *Daemon) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req authCredentialsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, d.daemonCfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	created, err := d.store.Login(r.Context(), req.Username, req.Password, sessionTTL)
	if errors.Is(err, store.ErrAuthInvalid) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		d.logger.Error().Err(err).Msg("auth login")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, created.Token, created.ExpiresAt)
	writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
}

func (d *Daemon) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := d.store.RevokeAuthTokenByHash(r.Context(), cookie.Value); err != nil {
			d.logger.Warn().Err(err).Msg("auth logout: revoke session")
		}
	}
	clearSessionCookie(w)
	writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
}

func (d *Daemon) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, identity.User, http.StatusOK)
}

func (d *Daemon) handleAuthUsersList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.currentIdentity(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	users, err := d.store.ListUsers(r.Context())
	if err != nil {
		d.logger.Error().Err(err).Msg("auth users: list")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, users, http.StatusOK)
}

func (d *Daemon) handleAuthUsersCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.currentIdentity(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, d.daemonCfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user, err := d.store.CreateUser(r.Context(), req.Username, req.Password)
	if errors.Is(err, store.ErrAuthInvalid) {
		http.Error(w, "invalid user request", http.StatusBadRequest)
		return
	}
	if errors.Is(err, store.ErrAuthConflict) {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}
	if err != nil {
		d.logger.Error().Err(err).Msg("auth users: create")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, user, http.StatusCreated)
}

func (d *Daemon) handleAuthTokensList(w http.ResponseWriter, r *http.Request) {
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tokens, err := d.store.ListAuthTokens(r.Context(), identity.User.ID)
	if err != nil {
		d.logger.Error().Err(err).Msg("auth tokens: list")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tokens, http.StatusOK)
}

func (d *Daemon) handleAuthTokensCreate(w http.ResponseWriter, r *http.Request) {
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, d.daemonCfg.HTTP.MaxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	created, err := d.store.CreateAPIToken(r.Context(), identity.User.ID, req.Name, req.ExpiresAt)
	if errors.Is(err, store.ErrAuthInvalid) {
		http.Error(w, "invalid token request", http.StatusBadRequest)
		return
	}
	if err != nil {
		d.logger.Error().Err(err).Msg("auth tokens: create")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, createTokenResponse{AuthToken: created.AuthToken, Token: created.Token}, http.StatusCreated)
}

func (d *Daemon) handleAuthTokenRevoke(w http.ResponseWriter, r *http.Request) {
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid token id", http.StatusBadRequest)
		return
	}
	if err := d.store.RevokeAuthToken(r.Context(), identity.User.ID, id); errors.Is(err, store.ErrAuthNotFound) {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	} else if err != nil {
		d.logger.Error().Err(err).Msg("auth tokens: revoke")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func setSessionCookie(w http.ResponseWriter, token string, expires *time.Time) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if expires != nil {
		cookie.Expires = *expires
	}
	http.SetCookie(w, cookie)
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
