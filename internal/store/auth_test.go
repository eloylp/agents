package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eloylp/agents/internal/store"
)

func TestAuthBootstrapLoginAndAPITokenLifecycle(t *testing.T) {
	t.Parallel()

	st := store.New(openTestDB(t))
	ctx := context.Background()

	created, err := st.BootstrapUser(ctx, "admin", "correct horse battery staple", 0)
	if err != nil {
		t.Fatalf("BootstrapUser() error = %v", err)
	}
	if created.Token == "" {
		t.Fatal("BootstrapUser() token is empty")
	}

	if _, err := st.BootstrapUser(ctx, "other", "password", 0); !errors.Is(err, store.ErrBootstrapClosed) {
		t.Fatalf("second BootstrapUser() error = %v, want %v", err, store.ErrBootstrapClosed)
	}

	if _, err := st.Login(ctx, "admin", "wrong", 0); !errors.Is(err, store.ErrAuthInvalid) {
		t.Fatalf("Login(wrong password) error = %v, want %v", err, store.ErrAuthInvalid)
	}

	session, err := st.Login(ctx, "admin", "correct horse battery staple", 0)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	identity, err := st.AuthenticateToken(ctx, session.Token, store.AuthTokenKindSession)
	if err != nil {
		t.Fatalf("AuthenticateToken(session) error = %v", err)
	}
	if identity.User.Username != "admin" {
		t.Fatalf("authenticated username = %q, want admin", identity.User.Username)
	}

	api, err := st.CreateAPIToken(ctx, identity.User.ID, "Codex MCP", nil)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if api.Token == "" || api.Prefix == "" {
		t.Fatalf("CreateAPIToken() token=%q prefix=%q, want both set", api.Token, api.Prefix)
	}
	tokens, err := st.ListAuthTokens(ctx, identity.User.ID)
	if err != nil {
		t.Fatalf("ListAuthTokens() error = %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("ListAuthTokens() len = %d, want 3", len(tokens))
	}
	for _, token := range tokens {
		if token.Prefix == api.Token {
			t.Fatalf("ListAuthTokens() exposed plaintext token %q", token.Prefix)
		}
	}

	if _, err := st.AuthenticateToken(ctx, api.Token, store.AuthTokenKindAPI); err != nil {
		t.Fatalf("AuthenticateToken(api) error = %v", err)
	}
	if err := st.RevokeAuthToken(ctx, identity.User.ID, api.ID); err != nil {
		t.Fatalf("RevokeAuthToken() error = %v", err)
	}
	if _, err := st.AuthenticateToken(ctx, api.Token, store.AuthTokenKindAPI); !errors.Is(err, store.ErrAuthInvalid) {
		t.Fatalf("AuthenticateToken(revoked api) error = %v, want %v", err, store.ErrAuthInvalid)
	}
}
