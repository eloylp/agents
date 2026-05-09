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
	router.Handle("/auth/users/{id}", withTimeout(http.HandlerFunc(d.handleAuthUserDelete))).Methods(http.MethodDelete)
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
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !identity.User.IsAdmin {
		http.Error(w, "admin required", http.StatusForbidden)
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

func (d *Daemon) handleAuthUserDelete(w http.ResponseWriter, r *http.Request) {
	identity, ok := d.currentIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !identity.User.IsAdmin {
		http.Error(w, "admin required", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if err := d.store.DeleteUser(r.Context(), id); errors.Is(err, store.ErrAuthForbidden) {
		http.Error(w, "admin user cannot be removed", http.StatusConflict)
		return
	} else if errors.Is(err, store.ErrAuthNotFound) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	} else if errors.Is(err, store.ErrAuthInvalid) {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	} else if err != nil {
		d.logger.Error().Err(err).Msg("auth users: delete")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func (d *Daemon) handleRootLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.authenticateRequest(r); ok {
		http.Redirect(w, r, "/ui/graph/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rootLoginHTML))
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

const rootLoginHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Agents login</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      min-height: 100vh;
      display: grid;
      place-items: center;
      overflow: hidden;
      padding: 20px;
      font-family: "SF Mono", "Fira Code", "Cascadia Code", Consolas, monospace;
      background:
        linear-gradient(135deg, rgba(7,17,31,0.82), rgba(13,34,55,0.58) 44%, rgba(238,246,255,0.18)),
        url("/ui/agents.jpg") center / cover no-repeat,
        linear-gradient(135deg, #07111f 0%, #0d2237 48%, #eef6ff 100%);
      color: #0f2742;
    }
    body::before {
      content: "";
      position: fixed;
      inset: 0;
      opacity: 0.32;
      background-image:
        linear-gradient(rgba(255,255,255,0.16) 1px, transparent 1px),
        linear-gradient(90deg, rgba(255,255,255,0.16) 1px, transparent 1px);
      background-size: 44px 44px;
      mask-image: linear-gradient(120deg, transparent 0%, black 18%, black 82%, transparent 100%);
    }
    .card {
      position: relative;
      width: min(430px, 100%);
      border: 1px solid rgba(255,255,255,0.55);
      border-radius: 0;
      background: rgba(255,255,255,0.88);
      backdrop-filter: blur(18px);
      padding: 22px;
      box-shadow: 0 30px 90px rgba(2,6,23,0.34);
    }
    .eyebrow {
      color: #2563eb;
      font-size: 0.72rem;
      font-weight: 800;
      letter-spacing: 0.14em;
      text-transform: uppercase;
      margin-bottom: 0.55rem;
    }
    h1 {
      color: #0f2742;
      font-size: 1.65rem;
      letter-spacing: -0.04em;
      line-height: 1.05;
      margin-bottom: 0.65rem;
    }
    p {
      color: #475569;
      font-size: 0.88rem;
      line-height: 1.55;
      margin-bottom: 1.15rem;
    }
    label {
      display: block;
      color: #1e3a5f;
      font-size: 0.76rem;
      font-weight: 700;
      margin-top: 0.75rem;
    }
    input {
      width: 100%;
      margin-top: 0.35rem;
      padding: 0.65rem 0.75rem;
      border: 1px solid #bfdbfe;
      border-radius: 0;
      background: #f8fafc;
      color: #1e293b;
      font: inherit;
    }
    button {
      width: 100%;
      margin-top: 1rem;
      padding: 0.7rem 0.9rem;
      border: 1px solid #1d4ed8;
      border-radius: 0;
      background: #2563eb;
      color: #fff;
      font: inherit;
      font-weight: 700;
      cursor: pointer;
    }
    button:disabled { cursor: wait; opacity: 0.72; }
    .status {
      border: 1px solid rgba(37,99,235,0.22);
      border-radius: 0;
      background: rgba(239,246,255,0.72);
      color: #1e3a5f;
      padding: 0.8rem;
      font-size: 0.82rem;
    }
    .error {
      color: #dc2626;
      font-size: 0.78rem;
      margin-top: 0.75rem;
      margin-bottom: 0;
    }
  </style>
</head>
<body>
  <main class="card">
    <div class="eyebrow">Agents dashboard</div>
    <h1 id="title">Checking session</h1>
    <p id="copy">Checking your browser session.</p>
    <div id="loading" class="status">Checking session...</div>
    <form id="form" hidden>
      <label>Username<input id="username" name="username" autocomplete="username" autofocus></label>
      <label>Password<input id="password" name="password" type="password" autocomplete="current-password"></label>
      <p id="error" class="error" hidden></p>
      <button id="submit" type="submit">Sign in</button>
    </form>
  </main>
  <script>
    const title = document.getElementById('title');
    const copy = document.getElementById('copy');
    const loading = document.getElementById('loading');
    const form = document.getElementById('form');
    const button = document.getElementById('submit');
    const error = document.getElementById('error');
    let bootstrapRequired = false;

    function openDashboard(path = '/ui/graph/') {
      try {
        window.top.location.assign(path);
      } catch {
        window.location.assign(path);
      }
      setTimeout(() => window.location.replace(path), 75);
    }

    async function refresh() {
      const res = await fetch('/auth/status', { cache: 'no-store', credentials: 'same-origin' });
      if (!res.ok) throw new Error('Could not check auth status.');
      const status = await res.json();
      if (status.authenticated) {
        openDashboard();
        return;
      }
      bootstrapRequired = Boolean(status.bootstrap_required);
      title.textContent = bootstrapRequired ? 'Create the first admin' : 'Sign in to your fleet';
      copy.textContent = bootstrapRequired
        ? 'Bootstrap the local admin account before exposing fleet configuration, traces, runners, and MCP tokens.'
        : 'Use your local dashboard account to access fleet configuration, traces, runners, and token management.';
      button.textContent = bootstrapRequired ? 'Create admin user' : 'Sign in';
      loading.hidden = true;
      form.hidden = false;
    }

    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      error.hidden = true;
      button.disabled = true;
      const path = bootstrapRequired ? '/auth/bootstrap' : '/auth/login';
      const res = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({
          username: document.getElementById('username').value,
          password: document.getElementById('password').value,
        }),
      });
      if (!res.ok) {
        button.disabled = false;
        error.textContent = bootstrapRequired ? 'Bootstrap failed.' : 'Login failed.';
        error.hidden = false;
        return;
      }
      button.textContent = 'Opening dashboard...';
      const statusRes = await fetch('/auth/status', { cache: 'no-store', credentials: 'same-origin' });
      const status = statusRes.ok ? await statusRes.json() : null;
      if (!status || !status.authenticated) {
        button.disabled = false;
        button.textContent = bootstrapRequired ? 'Create admin user' : 'Sign in';
        error.textContent = 'Signed in, but the browser session was not confirmed. Reload and try again.';
        error.hidden = false;
        return;
      }
      openDashboard(bootstrapRequired ? '/ui/setup/tooling/' : '/ui/graph/');
    });

    refresh().catch((err) => {
      title.textContent = 'Authentication unavailable';
      copy.textContent = err.message || 'Could not check the daemon auth state.';
      loading.hidden = true;
    });
  </script>
</body>
</html>`
