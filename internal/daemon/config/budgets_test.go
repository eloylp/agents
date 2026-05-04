package config

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog"

	rootconfig "github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/store"
)

func budgetHandler(t *testing.T, maxBody int64) (*Handler, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "budgets.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	h := New(st, rootconfig.DaemonConfig{
		HTTP: rootconfig.HTTPConfig{MaxBodyBytes: maxBody},
	}, zerolog.Nop())
	return h, st
}

func budgetRouter(h *Handler) http.Handler {
	r := mux.NewRouter()
	h.RegisterRoutes(r, func(next http.Handler) http.Handler { return next })
	return r
}

func TestTokenBudgetRESTPatchIsPartial(t *testing.T) {
	t.Parallel()
	h, st := budgetHandler(t, 1<<20)
	created, err := st.CreateTokenBudget(store.TokenBudget{
		ScopeKind:  "backend",
		ScopeName:  "claude",
		Period:     "daily",
		CapTokens:  100,
		AlertAtPct: 0,
		Enabled:    false,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	body := bytes.NewBufferString(`{"cap_tokens":250}`)
	req := httptest.NewRequest(http.MethodPatch, "/token_budgets/1", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	budgetRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d body=%q", rec.Code, rec.Body.String())
	}
	var got store.TokenBudget
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != created.ID || got.CapTokens != 250 || got.ScopeKind != "backend" || got.ScopeName != "claude" || got.Enabled {
		t.Fatalf("patched budget = %+v, want preserved fields and disabled state", got)
	}
}

func TestTokenBudgetRESTUsesBodyLimit(t *testing.T) {
	t.Parallel()
	h, _ := budgetHandler(t, 8)
	req := httptest.NewRequest(http.MethodPost, "/token_budgets", bytes.NewBufferString(`{"scope_kind":"global"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	budgetRouter(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST status = %d body=%q, want 413", rec.Code, rec.Body.String())
	}
}
