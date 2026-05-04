package daemon

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

func (d *Daemon) withBearerAuth(next http.Handler) http.Handler {
	hash := strings.TrimSpace(d.daemonCfg.Auth.BearerTokenHash)
	if hash == "" {
		return next
	}
	expected, err := hex.DecodeString(hash)
	if err != nil || len(expected) != sha256.Size {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !validBearerToken(expected, r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agents"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
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
	if path == "/" || path == "/ui" || path == "/ui/" || strings.HasPrefix(path, "/ui/") {
		return true
	}
	// The local-model proxy is often consumed by AI CLIs that cannot attach
	// this daemon's UI/MCP bearer token. Keep proxy auth at the upstream layer.
	if d.proxy != nil && (path == d.daemonCfg.Proxy.Path || path == "/v1/models") {
		return true
	}
	return false
}

func validBearerToken(expected []byte, header string) bool {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], expected) == 1
}
