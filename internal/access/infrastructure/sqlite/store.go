// Package sqlite is the SQLite CredentialStore (spec 8.2): credentials in
// their own access_* tables, never routed through feature repositories.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"app-maker/internal/access/domain"
	"app-maker/internal/kernel"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS access_passwords (
  login        TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL,
  hash         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS access_sessions (
  id_hash      TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL,
  csrf         TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  last_seen    TEXT NOT NULL,
  expires_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS access_sessions_principal ON access_sessions (principal_id);
CREATE TABLE IF NOT EXISTS access_api_keys (
  hash         TEXT PRIMARY KEY,
  prefix       TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  name         TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS access_api_keys_principal ON access_api_keys (principal_id);
`

// Store is a CredentialStore on SQLite.
type Store struct{ db *sql.DB }

var _ domain.CredentialStore = (*Store)(nil)

// New creates the tables if needed.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return kernel.ErrNotFound
	}
	return err
}

// PutPassword implements domain.CredentialStore.
func (s *Store) PutPassword(ctx context.Context, c domain.PasswordCredential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_passwords (login, principal_id, hash) VALUES (?, ?, ?)
		ON CONFLICT (login) DO UPDATE SET hash = excluded.hash WHERE principal_id = excluded.principal_id`,
		c.Login, c.PrincipalID.String(), c.Hash)
	if err != nil {
		return err
	}
	got, err := s.PasswordByLogin(ctx, c.Login)
	if err != nil {
		return err
	}
	if got.PrincipalID != c.PrincipalID {
		return &kernel.ConstraintError{Path: "/login", Code: "duplicate_login"}
	}
	return nil
}

// PasswordByLogin implements domain.CredentialStore.
func (s *Store) PasswordByLogin(ctx context.Context, login string) (domain.PasswordCredential, error) {
	var c domain.PasswordCredential
	var pid string
	err := s.db.QueryRowContext(ctx, `SELECT login, principal_id, hash FROM access_passwords WHERE login = ?`, login).Scan(&c.Login, &pid, &c.Hash)
	if err != nil {
		return c, notFound(err)
	}
	c.PrincipalID, err = kernel.ParseID(pid)
	return c, err
}

// HasPasswords implements domain.CredentialStore.
func (s *Store) HasPasswords(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM access_passwords`).Scan(&n)
	return n > 0, err
}

// CreateSession implements domain.CredentialStore.
func (s *Store) CreateSession(ctx context.Context, ss domain.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_sessions (id_hash, principal_id, csrf, created_at, last_seen, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ss.IDHash, ss.PrincipalID.String(), ss.CSRF, ts(ss.CreatedAt), ts(ss.LastSeen), ts(ss.ExpiresAt))
	return err
}

// Session implements domain.CredentialStore.
func (s *Store) Session(ctx context.Context, h string) (domain.Session, error) {
	var ss domain.Session
	var pid, created, seen, expires string
	err := s.db.QueryRowContext(ctx, `SELECT id_hash, principal_id, csrf, created_at, last_seen, expires_at FROM access_sessions WHERE id_hash = ?`, h).
		Scan(&ss.IDHash, &pid, &ss.CSRF, &created, &seen, &expires)
	if err != nil {
		return ss, notFound(err)
	}
	ss.PrincipalID, err = kernel.ParseID(pid)
	ss.CreatedAt, ss.LastSeen, ss.ExpiresAt = parseTS(created), parseTS(seen), parseTS(expires)
	return ss, err
}

// TouchSession implements domain.CredentialStore.
func (s *Store) TouchSession(ctx context.Context, h string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE access_sessions SET last_seen = ? WHERE id_hash = ?`, ts(at), h)
	return err
}

// DeleteSession implements domain.CredentialStore.
func (s *Store) DeleteSession(ctx context.Context, h string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_sessions WHERE id_hash = ?`, h)
	return err
}

// DeleteSessionsOf implements domain.CredentialStore.
func (s *Store) DeleteSessionsOf(ctx context.Context, p kernel.ID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_sessions WHERE principal_id = ?`, p.String())
	return err
}

// PutAPIKey implements domain.CredentialStore.
func (s *Store) PutAPIKey(ctx context.Context, k domain.APIKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_api_keys (hash, prefix, principal_id, name, created_at) VALUES (?, ?, ?, ?, ?)`,
		k.Hash, k.Prefix, k.PrincipalID.String(), k.Name, ts(k.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return &kernel.ConstraintError{Path: "/key", Code: "duplicate_key"}
	}
	return err
}

// APIKey implements domain.CredentialStore.
func (s *Store) APIKey(ctx context.Context, h string) (domain.APIKey, error) {
	var k domain.APIKey
	var pid, created string
	err := s.db.QueryRowContext(ctx, `SELECT hash, prefix, principal_id, name, created_at FROM access_api_keys WHERE hash = ?`, h).
		Scan(&k.Hash, &k.Prefix, &pid, &k.Name, &created)
	if err != nil {
		return k, notFound(err)
	}
	k.PrincipalID, err = kernel.ParseID(pid)
	k.CreatedAt = parseTS(created)
	return k, err
}

// DeleteAPIKeysOf implements domain.CredentialStore.
func (s *Store) DeleteAPIKeysOf(ctx context.Context, p kernel.ID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_api_keys WHERE principal_id = ?`, p.String())
	return err
}
