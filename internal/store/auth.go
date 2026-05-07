package store

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAuthNotFound    = errors.New("auth: not found")
	ErrAuthInvalid     = errors.New("auth: invalid credentials")
	ErrAuthConflict    = errors.New("auth: conflict")
	ErrAuthForbidden   = errors.New("auth: forbidden")
	ErrBootstrapClosed = errors.New("auth: bootstrap closed")
)

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	IsAdmin     bool       `json:"is_admin"`
}

type AuthToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type CreatedAuthToken struct {
	AuthToken
	Token string `json:"token"`
}

type AuthIdentity struct {
	User  User
	Token AuthToken
}

const (
	AuthTokenKindSession = "session"
	AuthTokenKindAPI     = "api"

	passwordHashIterations = 210000
	passwordHashBytes      = 32
	passwordSaltBytes      = 16
	randomTokenBytes       = 32
)

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return 0, fmt.Errorf("auth: count users: %w", err)
	}
	return count, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,username,created_at,updated_at,last_login_at,disabled_at,id=(SELECT MIN(id) FROM users)
		FROM users ORDER BY username ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("auth: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list users rows: %w", err)
	}
	return out, nil
}

func (s *Store) CreateUser(ctx context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return User{}, ErrAuthInvalid
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users(username,password_hash,created_at,updated_at)
		VALUES(?,?,datetime('now'),datetime('now'))`, username, hash)
	if err != nil {
		if isUniqueConstraintError(err) {
			return User{}, ErrAuthConflict
		}
		return User{}, fmt.Errorf("auth: create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("auth: user id: %w", err)
	}
	return s.GetUser(ctx, id)
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id,username,created_at,updated_at,last_login_at,disabled_at,id=(SELECT MIN(id) FROM users)
		FROM users WHERE id=?`, id)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrAuthNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrAuthInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth: begin delete user: %w", err)
	}
	defer tx.Rollback()

	var adminID int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MIN(id), 0) FROM users").Scan(&adminID); err != nil {
		return fmt.Errorf("auth: admin lookup: %w", err)
	}
	if adminID == id {
		return ErrAuthForbidden
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: delete rows: %w", err)
	}
	if n == 0 {
		return ErrAuthNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auth: commit delete user: %w", err)
	}
	return nil
}

func (s *Store) BootstrapUser(ctx context.Context, username, password string, sessionTTL time.Duration) (CreatedAuthToken, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return CreatedAuthToken{}, ErrAuthInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: begin bootstrap: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: count users: %w", err)
	}
	if count != 0 {
		return CreatedAuthToken{}, ErrBootstrapClosed
	}
	hash, err := hashPassword(password)
	if err != nil {
		return CreatedAuthToken{}, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users(username,password_hash,created_at,updated_at)
		VALUES(?,?,datetime('now'),datetime('now'))`, username, hash)
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: create user: %w", err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: user id: %w", err)
	}
	created, err := createTokenTx(ctx, tx, userID, AuthTokenKindSession, "Web session", sessionTTL)
	if err != nil {
		return CreatedAuthToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: commit bootstrap: %w", err)
	}
	return created, nil
}

func (s *Store) Login(ctx context.Context, username, password string, sessionTTL time.Duration) (CreatedAuthToken, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return CreatedAuthToken{}, ErrAuthInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: begin login: %w", err)
	}
	defer tx.Rollback()

	var userID int64
	var stored string
	var disabled sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id,password_hash,disabled_at FROM users WHERE username=?`, username).
		Scan(&userID, &stored, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return CreatedAuthToken{}, ErrAuthInvalid
	}
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: lookup user: %w", err)
	}
	if disabled.Valid || !verifyPassword(password, stored) {
		return CreatedAuthToken{}, ErrAuthInvalid
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET last_login_at=datetime('now'), updated_at=datetime('now') WHERE id=?", userID); err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: update login: %w", err)
	}
	created, err := createTokenTx(ctx, tx, userID, AuthTokenKindSession, "Web session", sessionTTL)
	if err != nil {
		return CreatedAuthToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: commit login: %w", err)
	}
	return created, nil
}

func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string, expiresAt *time.Time) (CreatedAuthToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CreatedAuthToken{}, ErrAuthInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: begin create token: %w", err)
	}
	defer tx.Rollback()

	if err := requireEnabledUser(ctx, tx, userID); err != nil {
		return CreatedAuthToken{}, err
	}
	created, err := createTokenTx(ctx, tx, userID, AuthTokenKindAPI, name, 0)
	if err != nil {
		return CreatedAuthToken{}, err
	}
	if expiresAt != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET expires_at=? WHERE id=?", expiresAt.UTC().Format(time.RFC3339), created.ID); err != nil {
			return CreatedAuthToken{}, fmt.Errorf("auth: set expiry: %w", err)
		}
		created.ExpiresAt = expiresAt
	}
	if err := tx.Commit(); err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: commit create token: %w", err)
	}
	return created, nil
}

func (s *Store) ListAuthTokens(ctx context.Context, userID int64) ([]AuthToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,user_id,kind,name,prefix,created_at,expires_at,last_used_at,revoked_at
		FROM auth_tokens WHERE user_id=? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	defer rows.Close()
	var out []AuthToken
	for rows.Next() {
		tok, err := scanAuthToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list tokens rows: %w", err)
	}
	return out, nil
}

func (s *Store) RevokeAuthToken(ctx context.Context, userID, tokenID int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE auth_tokens SET revoked_at=datetime('now') WHERE id=? AND user_id=? AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("auth: revoke token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke rows: %w", err)
	}
	if n == 0 {
		return ErrAuthNotFound
	}
	return nil
}

func isUniqueConstraintError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}

func (s *Store) RevokeAuthTokenByHash(ctx context.Context, token string) error {
	hash := hashToken(token)
	_, err := s.db.ExecContext(ctx, "UPDATE auth_tokens SET revoked_at=datetime('now') WHERE token_hash=? AND revoked_at IS NULL", hash)
	if err != nil {
		return fmt.Errorf("auth: revoke current token: %w", err)
	}
	return nil
}

func (s *Store) AuthenticateToken(ctx context.Context, token, kind string) (AuthIdentity, error) {
	if strings.TrimSpace(token) == "" {
		return AuthIdentity{}, ErrAuthInvalid
	}
	hash := hashToken(token)
	row := s.db.QueryRowContext(ctx, `
		SELECT
			u.id,u.username,u.created_at,u.updated_at,u.last_login_at,u.disabled_at,u.id=(SELECT MIN(id) FROM users),
			t.id,t.user_id,t.kind,t.name,t.prefix,t.created_at,t.expires_at,t.last_used_at,t.revoked_at
		FROM auth_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash=? AND t.kind=?`, hash, kind)
	identity, err := scanAuthIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthIdentity{}, ErrAuthInvalid
	}
	if err != nil {
		return AuthIdentity{}, err
	}
	if identity.User.DisabledAt != nil || identity.Token.RevokedAt != nil {
		return AuthIdentity{}, ErrAuthInvalid
	}
	if identity.Token.ExpiresAt != nil && !identity.Token.ExpiresAt.After(time.Now().UTC()) {
		return AuthIdentity{}, ErrAuthInvalid
	}
	_, err = s.db.ExecContext(ctx, "UPDATE auth_tokens SET last_used_at=datetime('now') WHERE id=?", identity.Token.ID)
	if err != nil {
		return AuthIdentity{}, fmt.Errorf("auth: update last used: %w", err)
	}
	return identity, nil
}

func hashPassword(password string) (string, error) {
	salt, err := randomBytes(passwordSaltBytes)
	if err != nil {
		return "", err
	}
	key, err := pbkdf2SHA256WithIter(password, salt, passwordHashIterations, passwordHashBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordHashIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func pbkdf2SHA256WithIter(password string, salt []byte, iter, keyLen int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iter, keyLen)
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2SHA256WithIter(password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func createTokenTx(ctx context.Context, tx *sql.Tx, userID int64, kind, name string, ttl time.Duration) (CreatedAuthToken, error) {
	plaintext, err := newPlaintextToken()
	if err != nil {
		return CreatedAuthToken{}, err
	}
	prefix := tokenPrefix(plaintext)
	hash := hashToken(plaintext)
	var expires any
	var expiresPtr *time.Time
	if ttl > 0 {
		t := time.Now().UTC().Add(ttl)
		expires = t.Format(time.RFC3339)
		expiresPtr = &t
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO auth_tokens(user_id,kind,name,token_hash,prefix,expires_at,created_at)
		VALUES(?,?,?,?,?,?,datetime('now'))`, userID, kind, name, hash, prefix, expires)
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: create token: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return CreatedAuthToken{}, fmt.Errorf("auth: token id: %w", err)
	}
	return CreatedAuthToken{
		AuthToken: AuthToken{ID: id, UserID: userID, Kind: kind, Name: name, Prefix: prefix, ExpiresAt: expiresPtr},
		Token:     plaintext,
	}, nil
}

func requireEnabledUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var disabled sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT disabled_at FROM users WHERE id=?", userID).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthNotFound
	}
	if err != nil {
		return fmt.Errorf("auth: lookup user: %w", err)
	}
	if disabled.Valid {
		return ErrAuthInvalid
	}
	return nil
}

func newPlaintextToken() (string, error) {
	b, err := randomBytes(randomTokenBytes)
	if err != nil {
		return "", err
	}
	return "agents_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenPrefix(token string) string {
	if len(token) <= 15 {
		return token
	}
	return token[:15]
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("auth: random bytes: %w", err)
	}
	return b, nil
}

type tokenScanner interface {
	Scan(dest ...any) error
}

func scanUser(row tokenScanner) (User, error) {
	var user User
	var isAdmin int
	var created, updated, lastLogin, disabled sql.NullString
	if err := row.Scan(&user.ID, &user.Username, &created, &updated, &lastLogin, &disabled, &isAdmin); err != nil {
		return User{}, err
	}
	user.CreatedAt = parseDBTime(created)
	user.UpdatedAt = parseDBTime(updated)
	user.LastLoginAt = parseDBTimePtr(lastLogin)
	user.DisabledAt = parseDBTimePtr(disabled)
	user.IsAdmin = isAdmin != 0
	return user, nil
}

func scanAuthToken(row tokenScanner) (AuthToken, error) {
	var tok AuthToken
	var created, expires, lastUsed, revoked sql.NullString
	if err := row.Scan(&tok.ID, &tok.UserID, &tok.Kind, &tok.Name, &tok.Prefix, &created, &expires, &lastUsed, &revoked); err != nil {
		return AuthToken{}, fmt.Errorf("auth: scan token: %w", err)
	}
	tok.CreatedAt = parseDBTime(created)
	tok.ExpiresAt = parseDBTimePtr(expires)
	tok.LastUsedAt = parseDBTimePtr(lastUsed)
	tok.RevokedAt = parseDBTimePtr(revoked)
	return tok, nil
}

func scanAuthIdentity(row tokenScanner) (AuthIdentity, error) {
	var ident AuthIdentity
	var isAdmin int
	var userCreated, userUpdated, lastLogin, disabled sql.NullString
	var tokCreated, expires, lastUsed, revoked sql.NullString
	err := row.Scan(
		&ident.User.ID, &ident.User.Username, &userCreated, &userUpdated, &lastLogin, &disabled, &isAdmin,
		&ident.Token.ID, &ident.Token.UserID, &ident.Token.Kind, &ident.Token.Name, &ident.Token.Prefix, &tokCreated, &expires, &lastUsed, &revoked,
	)
	if err != nil {
		return AuthIdentity{}, err
	}
	ident.User.CreatedAt = parseDBTime(userCreated)
	ident.User.UpdatedAt = parseDBTime(userUpdated)
	ident.User.LastLoginAt = parseDBTimePtr(lastLogin)
	ident.User.DisabledAt = parseDBTimePtr(disabled)
	ident.User.IsAdmin = isAdmin != 0
	ident.Token.CreatedAt = parseDBTime(tokCreated)
	ident.Token.ExpiresAt = parseDBTimePtr(expires)
	ident.Token.LastUsedAt = parseDBTimePtr(lastUsed)
	ident.Token.RevokedAt = parseDBTimePtr(revoked)
	return ident, nil
}

func parseDBTime(ns sql.NullString) time.Time {
	if t := parseDBTimePtr(ns); t != nil {
		return *t
	}
	return time.Time{}
}

func parseDBTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
