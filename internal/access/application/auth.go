package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/access/domain"
	contentapi "organon/internal/content/api"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// AuthConfig configures authentication (spec 8.3, 12.2 auth).
type AuthConfig struct {
	PasswordEnabled   bool
	MinPasswordLength int
	SessionIdle       time.Duration // default 24h
	SessionAbsolute   time.Duration // default 30 days
	// MFA (spec 8.3): TOTP required for all users or for these roles.
	MFAAll   bool
	MFARoles []string
	// Issuer names the app in authenticator apps.
	Issuer string
	// Sealer encrypts TOTP secrets with the instance data key; nil
	// disables enrollment.
	Sealer domain.SecretSealer
	// StateSealer encrypts the short-lived OIDC state cookie.
	StateSealer domain.SecretSealer
	// OIDC providers by name.
	OIDC map[string]OIDCConfig
	// Audit records authentication events (spec 14.3); optional.
	Audit func(ctx context.Context, recs []kernel.AuditRecord) error
	// NoAdmins refuses principals with the admin role and skips the
	// bootstrap admin (ADR 0017).
	NoAdmins bool
	// PublicURL prefixes share-link URLs (ADR 0020); empty gives
	// relative URLs.
	PublicURL string
}

// ErrAdminsDisabled is returned for admin principals when admin accounts
// are disabled.
var ErrAdminsDisabled = kernel.NewError(kernel.CodeForbidden, "admin accounts are disabled")

// OIDCConfig is one provider of app.json auth.oidc.
type OIDCConfig struct {
	Provider domain.OIDCProvider
	// Signup creates a user on the first login of an unlinked identity;
	// otherwise only identities linked through an admin invite log in
	// (decided 2026-11-01).
	Signup         bool
	AllowedDomains []string
}

// AuthService authenticates principals and manages credentials.
type AuthService struct {
	store   domain.CredentialStore
	hasher  domain.PasswordHasher
	limiter domain.LoginLimiter
	reader  contentapi.SystemReader
	writer  contentapi.SystemWriter
	index   domain.RelationshipIndex
	clock   kernel.Clock
	cfg     AuthConfig
	// dummyHash keeps unknown-login attempts as slow as wrong passwords.
	dummyHash string
}

// NewAuthService returns an AuthService.
func NewAuthService(store domain.CredentialStore, hasher domain.PasswordHasher, limiter domain.LoginLimiter,
	reader contentapi.SystemReader, writer contentapi.SystemWriter, index domain.RelationshipIndex, clock kernel.Clock, cfg AuthConfig) *AuthService {
	if cfg.SessionIdle == 0 {
		cfg.SessionIdle = 24 * time.Hour
	}
	if cfg.SessionAbsolute == 0 {
		cfg.SessionAbsolute = 30 * 24 * time.Hour
	}
	if cfg.MinPasswordLength == 0 {
		cfg.MinPasswordLength = 12
	}
	dummy, _ := hasher.Hash("dummy-password-for-timing")
	return &AuthService{store: store, hasher: hasher, limiter: limiter, reader: reader, writer: writer,
		index: index, clock: clock, cfg: cfg, dummyHash: dummy}
}

// ErrInvalidCredentials is the generic login failure (spec 8.3: never
// reveals whether the account exists).
var ErrInvalidCredentials = kernel.NewError(kernel.CodeUnauthenticated, "invalid login or password")

func normalizeLogin(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func randomToken(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failed
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the stored form of a session ID or API key.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// LoginResult is a new session, or a pending second factor: MFA is
// "verify" or "enroll" and Ticket continues the login.
type LoginResult struct {
	Token     string
	CSRF      string
	Principal accessapi.Principal
	ExpiresAt time.Time
	MFA       string
	Ticket    string
}

// Login checks a password and opens a session (spec 8.3).
func (s *AuthService) Login(ctx context.Context, login, password, ip string) (LoginResult, error) {
	if !s.cfg.PasswordEnabled {
		return LoginResult{}, kernel.NewError(kernel.CodeForbidden, "password login is disabled")
	}
	login = normalizeLogin(login)
	acct, ipKey := "acct:"+login, "ip:"+ip
	if !s.limiter.Allow(acct) || !s.limiter.Allow(ipKey) {
		return LoginResult{}, kernel.NewError(kernel.CodeRateLimited, "too many login attempts")
	}
	cred, err := s.store.PasswordByLogin(ctx, login)
	hash := cred.Hash
	if err != nil {
		hash = s.dummyHash
	}
	ok, verr := s.hasher.Verify(password, hash)
	if err != nil || verr != nil || !ok {
		s.limiter.Fail(acct)
		s.limiter.Fail(ipKey)
		s.audit(ctx, cred.PrincipalID, "login_failed", "password")
		return LoginResult{}, ErrInvalidCredentials
	}
	s.audit(ctx, cred.PrincipalID, "login", "password")
	p, err := s.Resolve(ctx, cred.PrincipalID)
	if errors.Is(err, ErrAdminsDisabled) {
		return LoginResult{}, err
	}
	if err != nil {
		s.limiter.Fail(acct)
		return LoginResult{}, ErrInvalidCredentials
	}
	s.limiter.Reset(acct)
	return s.finish(ctx, p)
}

// openSession creates a session for an authenticated principal.
func (s *AuthService) openSession(ctx context.Context, p accessapi.Principal) (LoginResult, error) {
	now := s.clock.Now()
	tok := randomToken("fws_", 32)
	sess := domain.Session{IDHash: HashToken(tok), PrincipalID: p.ID, CSRF: randomToken("", 24),
		CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(s.cfg.SessionAbsolute)}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: tok, CSRF: sess.CSRF, Principal: p, ExpiresAt: sess.ExpiresAt}, nil
}

// Logout ends a session.
func (s *AuthService) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, HashToken(token))
}

// AuthenticateSession resolves a session token; it returns the session's
// CSRF token for double-submit checks.
func (s *AuthService) AuthenticateSession(ctx context.Context, token string) (accessapi.Principal, string, error) {
	h := HashToken(token)
	sess, err := s.store.Session(ctx, h)
	if err != nil {
		return accessapi.Principal{}, "", kernel.ErrUnauthenticated
	}
	now := s.clock.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeen) > s.cfg.SessionIdle {
		_ = s.store.DeleteSession(ctx, h)
		return accessapi.Principal{}, "", kernel.ErrUnauthenticated
	}
	p, err := s.Resolve(ctx, sess.PrincipalID)
	if err != nil {
		return accessapi.Principal{}, "", kernel.ErrUnauthenticated
	}
	if now.Sub(sess.LastSeen) > time.Minute {
		_ = s.store.TouchSession(ctx, h, now)
	}
	return p, sess.CSRF, nil
}

// AuthenticateAPIKey resolves a bearer API key.
func (s *AuthService) AuthenticateAPIKey(ctx context.Context, key string) (accessapi.Principal, error) {
	k, err := s.store.APIKey(ctx, HashToken(key))
	if err != nil {
		return accessapi.Principal{}, kernel.ErrUnauthenticated
	}
	p, err := s.Resolve(ctx, k.PrincipalID)
	if err != nil {
		return accessapi.Principal{}, kernel.ErrUnauthenticated
	}
	return p, nil
}

// Resolve loads a principal from its feature: kind, roles, status, and
// transitive groups. Suspended or deleted principals cannot act.
func (s *AuthService) Resolve(ctx context.Context, id kernel.ID) (accessapi.Principal, error) {
	f, err := s.reader.Get(ctx, id)
	if err != nil {
		return accessapi.Principal{}, err
	}
	base := f.Type.Base()
	if base == schemaapi.TypeShareLink {
		// Flows caused by a link holder's action run as that holder.
		return linkPrincipal(id, f.Data)
	}
	if base != schemaapi.TypeUser && base != schemaapi.TypeServiceAccount {
		return accessapi.Principal{}, kernel.ErrForbidden
	}
	if st, _ := f.Data["status"].(string); st != "" && st != "active" {
		return accessapi.Principal{}, kernel.NewError(kernel.CodeForbidden, "principal is %s", st)
	}
	kind, _ := f.Data["kind"].(string)
	p := accessapi.Principal{ID: id, Kind: accessapi.PrincipalKind(kind)}
	p.Locale, _ = f.Data["locale"].(string)
	if roles, ok := f.Data["roles"].([]any); ok {
		for _, r := range roles {
			if s, ok := r.(string); ok {
				p.Roles = append(p.Roles, s)
			}
		}
	}
	if s.cfg.NoAdmins && p.HasRole("admin") {
		return accessapi.Principal{}, ErrAdminsDisabled
	}
	if p.Groups, err = s.index.Groups(ctx, id); err != nil {
		return accessapi.Principal{}, err
	}
	return p, nil
}

// NewUser describes a user created by an admin or at bootstrap.
type NewUser struct {
	Email       string
	DisplayName string
	Password    string
	Roles       []string
}

// CreateUser creates a user feature and its password credential. Only
// admins may create users (no self-signup; decided 2026-10-31).
func (s *AuthService) CreateUser(ctx context.Context, actor accessapi.Principal, u NewUser) (kernel.ID, error) {
	if !actor.IsSystem() && !actor.HasRole("admin") {
		return kernel.NilID, kernel.ErrForbidden
	}
	if s.cfg.NoAdmins && slices.Contains(u.Roles, "admin") {
		return kernel.NilID, ErrAdminsDisabled
	}
	if len(u.Password) < s.cfg.MinPasswordLength {
		return kernel.NilID, kernel.NewValidationError(kernel.FieldError{Path: "/password", Code: "minLength",
			Message: "password is too short"})
	}
	login := normalizeLogin(u.Email)
	if _, err := s.store.PasswordByLogin(ctx, login); err == nil {
		return kernel.NilID, &kernel.ConstraintError{Path: "/email", Code: "duplicate_login"}
	}
	hash, err := s.hasher.Hash(u.Password)
	if err != nil {
		return kernel.NilID, err
	}
	data := map[string]any{"email": login, "displayName": u.DisplayName}
	if len(u.Roles) > 0 {
		roles := make([]any, len(u.Roles))
		for i, r := range u.Roles {
			roles[i] = r
		}
		data["roles"] = roles
	}
	id, err := s.writer.Create(ctx, "user", nil, data, nil)
	if err != nil {
		return kernel.NilID, err
	}
	if err := s.store.PutPassword(ctx, domain.PasswordCredential{PrincipalID: id, Login: login, Hash: hash}); err != nil {
		return kernel.NilID, err
	}
	return id, nil
}

// CreateAPIKey issues a key for a service account the actor owns, or for
// any principal when the actor is an admin. The key is returned once.
func (s *AuthService) CreateAPIKey(ctx context.Context, actor accessapi.Principal, principal kernel.ID, name string) (string, error) {
	target, err := s.reader.Get(ctx, principal)
	if err != nil {
		return "", err
	}
	owner, _ := target.Data["owner"].(string)
	if !actor.IsSystem() && !actor.HasRole("admin") && owner != actor.ID.String() && principal != actor.ID {
		return "", kernel.ErrForbidden
	}
	prefix := randomToken("", 6)
	key := "fwk_" + prefix + "_" + randomToken("", 32)
	err = s.store.PutAPIKey(ctx, domain.APIKey{Prefix: prefix, Hash: HashToken(key), PrincipalID: principal, Name: name, CreatedAt: s.clock.Now()})
	if err != nil {
		return "", err
	}
	s.audit(ctx, actor.ID, "api_key_created", "for "+principal.String()+" prefix "+prefix)
	return key, nil
}

// BootstrapAdmin creates the initial admin user when no password
// credential exists yet (spec 8.4). It is a no-op afterwards.
func (s *AuthService) BootstrapAdmin(ctx context.Context, email, password string) (bool, error) {
	if s.cfg.NoAdmins {
		return false, nil
	}
	has, err := s.store.HasPasswords(ctx)
	if err != nil || has {
		return false, err
	}
	if email == "" || password == "" {
		return false, errors.New("no admin credentials: set ORGANON_BOOTSTRAP_ADMIN_EMAIL and ORGANON_BOOTSTRAP_ADMIN_PASSWORD")
	}
	_, err = s.CreateUser(ctx, accessapi.System(), NewUser{Email: email, DisplayName: "Administrator", Password: password, Roles: []string{"admin"}})
	var ce *kernel.ConstraintError
	if errors.As(err, &ce) {
		return false, nil // another replica won the race
	}
	return err == nil, err
}

// audit records an authentication event; failures do not block logins.
func (s *AuthService) audit(ctx context.Context, actor kernel.ID, action, detail string) {
	if s.cfg.Audit == nil {
		return
	}
	_ = s.cfg.Audit(ctx, []kernel.AuditRecord{{At: s.clock.Now(), Actor: actor, ActorKind: string(accessapi.KindUser), Action: action, Detail: detail}})
}
