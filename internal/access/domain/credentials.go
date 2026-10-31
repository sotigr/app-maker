package domain

import (
	"context"
	"time"

	"app-maker/internal/kernel"
)

// PasswordCredential is a password login (spec 8.2). Hash encodes the
// Argon2id parameters with the hash for future upgrades.
type PasswordCredential struct {
	PrincipalID kernel.ID
	Login       string // normalized (lower-case) email
	Hash        string
}

// Session is a browser session; only the hash of its ID is stored.
type Session struct {
	IDHash      string
	PrincipalID kernel.ID
	CSRF        string
	CreatedAt   time.Time
	LastSeen    time.Time
	ExpiresAt   time.Time // absolute expiry
}

// APIKey is a service-account or CLI key; shown once, stored as SHA-256.
type APIKey struct {
	Prefix      string
	Hash        string
	PrincipalID kernel.ID
	Name        string
	CreatedAt   time.Time
}

// CredentialStore is the sealed credential repository (spec 8.2). It is
// never routed through feature repositories, change log, flows, or search.
type CredentialStore interface {
	PutPassword(ctx context.Context, c PasswordCredential) error
	PasswordByLogin(ctx context.Context, login string) (PasswordCredential, error)
	HasPasswords(ctx context.Context) (bool, error)
	CreateSession(ctx context.Context, s Session) error
	Session(ctx context.Context, idHash string) (Session, error)
	TouchSession(ctx context.Context, idHash string, at time.Time) error
	DeleteSession(ctx context.Context, idHash string) error
	DeleteSessionsOf(ctx context.Context, principal kernel.ID) error
	PutAPIKey(ctx context.Context, k APIKey) error
	APIKey(ctx context.Context, hash string) (APIKey, error)
	DeleteAPIKeysOf(ctx context.Context, principal kernel.ID) error
}

// PasswordHasher hashes and verifies passwords (Argon2id).
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Verify compares in constant time.
	Verify(password, encoded string) (bool, error)
}

// LoginLimiter limits login attempts per account and per IP (spec 8.3, 14.1).
type LoginLimiter interface {
	// Allow reports whether another attempt for key is allowed now.
	Allow(key string) bool
	Fail(key string)
	Reset(key string)
}

// Grant is one (subject, role, object) tuple of the relationship index.
type Grant struct {
	Subject kernel.ID
	Object  kernel.ID
	Role    string
}

// RelationshipIndex answers membership and grant lookups (spec 8.6).
type RelationshipIndex interface {
	// Groups returns the transitive group memberships of a principal.
	Groups(ctx context.Context, principal kernel.ID) ([]kernel.ID, error)
	// Grants returns all grants held by any of the subjects.
	Grants(ctx context.Context, subjects []kernel.ID) ([]Grant, error)
}
