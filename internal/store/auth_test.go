package store_test

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eloylp/agents/internal/store"
	"golang.org/x/crypto/bcrypt"
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
	other, err := st.CreateUser(ctx, "other", "password")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if other.Username != "other" {
		t.Fatalf("CreateUser() username = %q, want other", other.Username)
	}
	if _, err := st.CreateUser(ctx, "other", "password"); !errors.Is(err, store.ErrAuthConflict) {
		t.Fatalf("CreateUser(duplicate) error = %v, want %v", err, store.ErrAuthConflict)
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("ListUsers() len = %d, want 2", len(users))
	}
	for _, user := range users {
		if user.Username == "admin" && !user.IsAdmin {
			t.Fatal("bootstrap user is not marked admin")
		}
		if user.Username == "other" && user.IsAdmin {
			t.Fatal("non-bootstrap user is marked admin")
		}
	}
	if err := st.DeleteUser(ctx, created.UserID); !errors.Is(err, store.ErrAuthForbidden) {
		t.Fatalf("DeleteUser(admin) error = %v, want %v", err, store.ErrAuthForbidden)
	}
	if err := st.DeleteUser(ctx, other.ID); err != nil {
		t.Fatalf("DeleteUser(other) error = %v", err)
	}
	users, err = st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers(after delete) error = %v", err)
	}
	if len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("ListUsers(after delete) = %+v, want only admin", users)
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
	if !identity.User.IsAdmin {
		t.Fatal("AuthenticateToken(session) user is not marked admin")
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

func TestAuthAdminStatusComesFromDatabase(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()

	created, err := st.BootstrapUser(ctx, "bootstrap", "correct horse battery staple", 0)
	if err != nil {
		t.Fatalf("BootstrapUser() error = %v", err)
	}
	other, err := st.CreateUser(ctx, "operator", "correct horse battery staple")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if _, err := db.Exec("UPDATE users SET is_admin = CASE WHEN id = ? THEN 1 ELSE 0 END", other.ID); err != nil {
		t.Fatalf("update admin flags: %v", err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	for _, user := range users {
		switch user.ID {
		case created.UserID:
			if user.IsAdmin {
				t.Fatalf("bootstrap user IsAdmin = true after DB flag cleared")
			}
		case other.ID:
			if !user.IsAdmin {
				t.Fatalf("operator IsAdmin = false after DB flag set")
			}
		}
	}
	got, err := st.GetUser(ctx, other.ID)
	if err != nil {
		t.Fatalf("GetUser(operator) error = %v", err)
	}
	if !got.IsAdmin {
		t.Fatal("GetUser(operator) IsAdmin = false, want true")
	}
	session, err := st.Login(ctx, "operator", "correct horse battery staple", 0)
	if err != nil {
		t.Fatalf("Login(operator) error = %v", err)
	}
	identity, err := st.AuthenticateToken(ctx, session.Token, store.AuthTokenKindSession)
	if err != nil {
		t.Fatalf("AuthenticateToken(operator) error = %v", err)
	}
	if !identity.User.IsAdmin {
		t.Fatal("AuthenticateToken(operator) IsAdmin = false, want true")
	}
}

func TestAuthDeleteAdminAllowsAnotherAdmin(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()

	created, err := st.BootstrapUser(ctx, "admin", "correct horse battery staple", 0)
	if err != nil {
		t.Fatalf("BootstrapUser() error = %v", err)
	}
	other, err := st.CreateUser(ctx, "second-admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if _, err := db.Exec("UPDATE users SET is_admin = 1 WHERE id = ?", other.ID); err != nil {
		t.Fatalf("promote second admin: %v", err)
	}

	if err := st.DeleteUser(ctx, created.UserID); err != nil {
		t.Fatalf("DeleteUser(first admin) error = %v", err)
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 1 || users[0].ID != other.ID || !users[0].IsAdmin {
		t.Fatalf("remaining users = %+v, want only second admin", users)
	}
	if err := st.DeleteUser(ctx, other.ID); !errors.Is(err, store.ErrAuthForbidden) {
		t.Fatalf("DeleteUser(last admin) error = %v, want %v", err, store.ErrAuthForbidden)
	}
}

func TestAuthNewUsersStoreBcryptHashes(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "bcrypt-user", "correct horse battery staple")
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	hash := passwordHashForUser(t, db, user.ID)
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password hash prefix = %q, want bcrypt", hash)
	}
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		t.Fatalf("bcrypt.Cost() error = %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), bcryptPasswordInput("correct horse battery staple")); err != nil {
		t.Fatalf("bcrypt password verification error = %v", err)
	}

	if _, err := st.Login(ctx, "bcrypt-user", "correct horse battery staple", 0); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if got := passwordHashForUser(t, db, user.ID); got != hash {
		t.Fatalf("password hash changed after bcrypt login: got %q, want %q", got, hash)
	}
}

func TestAuthLongPasswordsStoreAndLoginWithBcrypt(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()
	password := strings.Repeat("long-password-", 8)

	user, err := st.CreateUser(ctx, "long-bcrypt-user", password)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	hash := passwordHashForUser(t, db, user.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), bcryptPasswordInput(password)); err != nil {
		t.Fatalf("bcrypt password verification error = %v", err)
	}
	if _, err := st.Login(ctx, "long-bcrypt-user", password, 0); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
}

func TestAuthLegacyPBKDF2LoginUpgradesToBcrypt(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()
	const password = "legacy password"
	legacyHash := legacyPBKDF2Hash(t, password)
	userID := insertUserWithPasswordHash(t, db, "legacy-user", legacyHash)

	if _, err := st.Login(ctx, "legacy-user", password, 0); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	upgraded := passwordHashForUser(t, db, userID)
	if upgraded == legacyHash {
		t.Fatal("password hash was not upgraded")
	}
	if strings.HasPrefix(upgraded, "pbkdf2-sha256$") {
		t.Fatalf("password hash still has legacy prefix: %q", upgraded)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(upgraded), bcryptPasswordInput(password)); err != nil {
		t.Fatalf("upgraded bcrypt verification error = %v", err)
	}
}

func TestAuthLongLegacyPBKDF2LoginUpgradesToBcrypt(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()
	password := strings.Repeat("legacy-long-password-", 5)
	legacyHash := legacyPBKDF2Hash(t, password)
	userID := insertUserWithPasswordHash(t, db, "legacy-long-user", legacyHash)

	if _, err := st.Login(ctx, "legacy-long-user", password, 0); err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	upgraded := passwordHashForUser(t, db, userID)
	if upgraded == legacyHash {
		t.Fatal("password hash was not upgraded")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(upgraded), bcryptPasswordInput(password)); err != nil {
		t.Fatalf("upgraded bcrypt verification error = %v", err)
	}
	if _, err := st.Login(ctx, "legacy-long-user", password, 0); err != nil {
		t.Fatalf("second Login() error = %v", err)
	}
}

func TestAuthInvalidAndUnknownPasswordHashesFailClosed(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	st := store.New(db)
	ctx := context.Background()
	legacyHash := legacyPBKDF2Hash(t, "correct password")
	legacyID := insertUserWithPasswordHash(t, db, "legacy-invalid", legacyHash)
	unknownID := insertUserWithPasswordHash(t, db, "unknown-format", "sha1$not-supported")

	if _, err := st.Login(ctx, "legacy-invalid", "wrong password", 0); !errors.Is(err, store.ErrAuthInvalid) {
		t.Fatalf("Login(wrong legacy password) error = %v, want %v", err, store.ErrAuthInvalid)
	}
	if got := passwordHashForUser(t, db, legacyID); got != legacyHash {
		t.Fatalf("legacy hash changed after failed login: got %q, want %q", got, legacyHash)
	}
	if _, err := st.Login(ctx, "unknown-format", "correct password", 0); !errors.Is(err, store.ErrAuthInvalid) {
		t.Fatalf("Login(unknown hash) error = %v, want %v", err, store.ErrAuthInvalid)
	}
	if got := passwordHashForUser(t, db, unknownID); got != "sha1$not-supported" {
		t.Fatalf("unknown hash changed after failed login: got %q", got)
	}
}

func legacyPBKDF2Hash(t *testing.T, password string) string {
	t.Helper()

	salt := []byte("fixed-test-salt!")
	key, err := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if err != nil {
		t.Fatalf("pbkdf2.Key() error = %v", err)
	}
	return fmt.Sprintf(
		"pbkdf2-sha256$210000$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

func bcryptPasswordInput(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	return sum[:]
}

func insertUserWithPasswordHash(t *testing.T, db *sql.DB, username, hash string) int64 {
	t.Helper()

	res, err := db.Exec(
		"INSERT INTO users(username,password_hash,created_at,updated_at) VALUES(?,?,datetime('now'),datetime('now'))",
		username,
		hash,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}
	return id
}

func passwordHashForUser(t *testing.T, db *sql.DB, userID int64) string {
	t.Helper()

	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id=?", userID).Scan(&hash); err != nil {
		t.Fatalf("query password hash: %v", err)
	}
	return hash
}
