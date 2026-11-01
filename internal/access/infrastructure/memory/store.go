// Package memory holds in-memory Access adapters: the credential store and
// the login limiter. They are refused in the replicated topology.
package memory

import (
	"context"
	"sync"
	"time"

	"app-maker/internal/access/domain"
	"app-maker/internal/kernel"
)

// Store is an in-memory CredentialStore.
type Store struct {
	mu         sync.RWMutex
	passwords  map[string]domain.PasswordCredential
	sessions   map[string]domain.Session
	keys       map[string]domain.APIKey
	identities map[[2]string]domain.Identity
	totp       map[kernel.ID]domain.TOTP
	tickets    map[string]domain.Ticket
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{passwords: map[string]domain.PasswordCredential{}, sessions: map[string]domain.Session{}, keys: map[string]domain.APIKey{},
		identities: map[[2]string]domain.Identity{}, totp: map[kernel.ID]domain.TOTP{}, tickets: map[string]domain.Ticket{}}
}

var _ domain.CredentialStore = (*Store)(nil)

// PutPassword implements domain.CredentialStore.
func (s *Store) PutPassword(_ context.Context, c domain.PasswordCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.passwords[c.Login]; ok && existing.PrincipalID != c.PrincipalID {
		return &kernel.ConstraintError{Path: "/login", Code: "duplicate_login"}
	}
	s.passwords[c.Login] = c
	return nil
}

// PasswordByLogin implements domain.CredentialStore.
func (s *Store) PasswordByLogin(_ context.Context, login string) (domain.PasswordCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.passwords[login]
	if !ok {
		return c, kernel.ErrNotFound
	}
	return c, nil
}

// HasPasswords implements domain.CredentialStore.
func (s *Store) HasPasswords(context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.passwords) > 0, nil
}

// CreateSession implements domain.CredentialStore.
func (s *Store) CreateSession(_ context.Context, ss domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[ss.IDHash] = ss
	return nil
}

// Session implements domain.CredentialStore.
func (s *Store) Session(_ context.Context, h string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ss, ok := s.sessions[h]
	if !ok {
		return ss, kernel.ErrNotFound
	}
	return ss, nil
}

// TouchSession implements domain.CredentialStore.
func (s *Store) TouchSession(_ context.Context, h string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[h]; ok {
		ss.LastSeen = at
		s.sessions[h] = ss
	}
	return nil
}

// DeleteSession implements domain.CredentialStore.
func (s *Store) DeleteSession(_ context.Context, h string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, h)
	return nil
}

// DeleteSessionsOf implements domain.CredentialStore.
func (s *Store) DeleteSessionsOf(_ context.Context, p kernel.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, ss := range s.sessions {
		if ss.PrincipalID == p {
			delete(s.sessions, h)
		}
	}
	return nil
}

// PutAPIKey implements domain.CredentialStore.
func (s *Store) PutAPIKey(_ context.Context, k domain.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[k.Hash] = k
	return nil
}

// APIKey implements domain.CredentialStore.
func (s *Store) APIKey(_ context.Context, h string) (domain.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[h]
	if !ok {
		return k, kernel.ErrNotFound
	}
	return k, nil
}

// DeleteAPIKeysOf implements domain.CredentialStore.
func (s *Store) DeleteAPIKeysOf(_ context.Context, p kernel.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, k := range s.keys {
		if k.PrincipalID == p {
			delete(s.keys, h)
		}
	}
	return nil
}

// Limiter is a sliding-window login limiter: Max failures per Window per
// key, then refused until the window passes (spec 14.1: 10 per 15 min).
type Limiter struct {
	mu     sync.Mutex
	Max    int
	Window time.Duration
	clock  kernel.Clock
	fails  map[string][]time.Time
}

// NewLimiter returns a limiter.
func NewLimiter(max int, window time.Duration, clock kernel.Clock) *Limiter {
	return &Limiter{Max: max, Window: window, clock: clock, fails: map[string][]time.Time{}}
}

var _ domain.LoginLimiter = (*Limiter)(nil)

func (l *Limiter) prune(key string) []time.Time {
	cut := l.clock.Now().Add(-l.Window)
	var keep []time.Time
	for _, t := range l.fails[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fails[key] = keep
	return keep
}

// Allow implements domain.LoginLimiter.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) < l.Max
}

// Fail implements domain.LoginLimiter.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.prune(key), l.clock.Now())
}

// Reset implements domain.LoginLimiter.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// PutIdentity implements domain.CredentialStore.
func (s *Store) PutIdentity(_ context.Context, i domain.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{i.Provider, i.Subject}
	if existing, ok := s.identities[k]; ok && existing.PrincipalID != i.PrincipalID {
		return &kernel.ConstraintError{Path: "/identity", Code: "duplicate_identity"}
	}
	s.identities[k] = i
	return nil
}

// Identity implements domain.CredentialStore.
func (s *Store) Identity(_ context.Context, provider, subject string) (domain.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.identities[[2]string{provider, subject}]
	if !ok {
		return i, kernel.ErrNotFound
	}
	return i, nil
}

// PutTOTP implements domain.CredentialStore.
func (s *Store) PutTOTP(_ context.Context, t domain.TOTP) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totp[t.PrincipalID] = t
	return nil
}

// TOTP implements domain.CredentialStore.
func (s *Store) TOTP(_ context.Context, p kernel.ID) (domain.TOTP, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.totp[p]
	if !ok {
		return t, kernel.ErrNotFound
	}
	return t, nil
}

// DeleteTOTP implements domain.CredentialStore.
func (s *Store) DeleteTOTP(_ context.Context, p kernel.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.totp, p)
	return nil
}

// CreateTicket implements domain.CredentialStore.
func (s *Store) CreateTicket(_ context.Context, t domain.Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.IDHash] = t
	return nil
}

// TakeTicket implements domain.CredentialStore.
func (s *Store) TakeTicket(_ context.Context, h string) (domain.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[h]
	if !ok {
		return t, kernel.ErrNotFound
	}
	delete(s.tickets, h)
	return t, nil
}
