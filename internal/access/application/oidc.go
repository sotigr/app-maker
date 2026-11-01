package application

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/access/domain"
	"app-maker/internal/kernel"
)

// InviteTTL is how long an OIDC invite link stays valid.
const InviteTTL = 7 * 24 * time.Hour

// OIDCState travels in an encrypted, short-lived cookie between start and
// callback (spec 14.10: never in server memory).
type OIDCState struct {
	Provider string    `json:"p"`
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Invite   string    `json:"i,omitempty"`
	Redirect string    `json:"r,omitempty"`
	Expires  time.Time `json:"e"`
}

// OIDCProviders returns the configured provider names.
func (s *AuthService) OIDCProviders() []string {
	names := make([]string, 0, len(s.cfg.OIDC))
	for n := range s.cfg.OIDC {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// OIDCStart begins the authorization code flow with PKCE. It returns the
// provider URL and the sealed state for the cookie.
func (s *AuthService) OIDCStart(ctx context.Context, provider, redirectURI, after, invite string) (string, string, error) {
	pc, ok := s.cfg.OIDC[provider]
	if !ok || s.cfg.StateSealer == nil {
		return "", "", kernel.ErrNotFound
	}
	st := OIDCState{Provider: provider, State: randomToken("", 24), Nonce: randomToken("", 24), Verifier: randomToken("", 48),
		Invite: invite, Redirect: after, Expires: s.clock.Now().Add(10 * time.Minute)}
	sum := sha256.Sum256([]byte(st.Verifier))
	u, err := pc.Provider.AuthURL(ctx, st.State, st.Nonce, base64.RawURLEncoding.EncodeToString(sum[:]), redirectURI)
	if err != nil {
		return "", "", kernel.NewError(kernel.CodeInternal, "identity provider unavailable: %v", err)
	}
	raw, _ := json.Marshal(st)
	sealed, err := s.cfg.StateSealer.Seal(raw)
	if err != nil {
		return "", "", err
	}
	return u, base64.RawURLEncoding.EncodeToString(sealed), nil
}

// ErrNoLinkedAccount: the identity is not linked and signup is off.
var ErrNoLinkedAccount = kernel.NewError(kernel.CodeUnauthenticated, "no account is linked to this identity; ask an administrator for an invite link")

// OIDCCallback finishes the flow: it checks state, redeems the code, and
// logs in the linked principal; unlinked identities are bound through an
// invite or, when the provider allows signup, get a new user. Matching is
// by provider and subject, never by email (spec 8.3).
func (s *AuthService) OIDCCallback(ctx context.Context, provider, sealedState, code, state, redirectURI string) (LoginResult, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealedState)
	if err != nil || s.cfg.StateSealer == nil {
		return LoginResult{}, "", kernel.NewError(kernel.CodeUnauthenticated, "the login expired; start again")
	}
	plain, err := s.cfg.StateSealer.Open(raw)
	var st OIDCState
	if err != nil || json.Unmarshal(plain, &st) != nil || st.Provider != provider || s.clock.Now().After(st.Expires) ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(state)) != 1 {
		return LoginResult{}, "", kernel.NewError(kernel.CodeUnauthenticated, "the login expired or was tampered with; start again")
	}
	pc, ok := s.cfg.OIDC[provider]
	if !ok {
		return LoginResult{}, "", kernel.ErrNotFound
	}
	claims, err := pc.Provider.Exchange(ctx, code, st.Verifier, redirectURI, st.Nonce)
	if err != nil {
		return LoginResult{}, st.Redirect, kernel.NewError(kernel.CodeUnauthenticated, "identity provider login failed: %v", err)
	}
	pid, err := s.linkedPrincipal(ctx, provider, pc, st.Invite, claims)
	if err != nil {
		return LoginResult{}, st.Redirect, err
	}
	p, err := s.Resolve(ctx, pid)
	if err != nil {
		return LoginResult{}, st.Redirect, ErrNoLinkedAccount
	}
	s.audit(ctx, pid, "login", "oidc:"+provider)
	res, err := s.finish(ctx, p)
	return res, st.Redirect, err
}

func (s *AuthService) linkedPrincipal(ctx context.Context, provider string, pc OIDCConfig, invite string, c domain.ExternalClaims) (kernel.ID, error) {
	id, err := s.store.Identity(ctx, provider, c.Subject)
	if err == nil {
		return id.PrincipalID, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return kernel.NilID, err
	}
	link := func(pid kernel.ID) (kernel.ID, error) {
		err := s.store.PutIdentity(ctx, domain.Identity{Provider: provider, Subject: c.Subject, PrincipalID: pid, CreatedAt: s.clock.Now()})
		if err == nil {
			s.audit(ctx, pid, "identity_linked", "oidc:"+provider)
		}
		return pid, err
	}
	if invite != "" {
		t, err := s.store.TakeTicket(ctx, HashToken(invite))
		if err != nil || t.Purpose != domain.TicketOIDCInvite || s.clock.Now().After(t.ExpiresAt) {
			return kernel.NilID, kernel.NewError(kernel.CodeUnauthenticated, "the invite link is invalid or expired")
		}
		return link(t.PrincipalID)
	}
	if !pc.Signup {
		return kernel.NilID, ErrNoLinkedAccount
	}
	if len(pc.AllowedDomains) > 0 {
		_, domainPart, _ := strings.Cut(c.Email, "@")
		if !c.EmailVerified || !slices.Contains(pc.AllowedDomains, domainPart) {
			return kernel.NilID, kernel.NewError(kernel.CodeForbidden, "accounts from this email domain cannot sign up")
		}
	}
	data := map[string]any{"displayName": c.Name}
	if c.Email != "" && c.EmailVerified {
		data["email"] = c.Email
	}
	pid, err := s.writer.Create(ctx, "user", nil, data, nil)
	if err != nil {
		return kernel.NilID, err
	}
	return link(pid)
}

// CreateOIDCInvite issues a one-time link that binds the first OIDC login
// to an existing user (admins only).
func (s *AuthService) CreateOIDCInvite(ctx context.Context, actor accessapi.Principal, user kernel.ID) (string, time.Time, error) {
	if !actor.IsSystem() && !actor.HasRole("admin") {
		return "", time.Time{}, kernel.ErrForbidden
	}
	p, err := s.Resolve(ctx, user)
	if err != nil {
		return "", time.Time{}, err
	}
	if p.Kind != accessapi.KindUser {
		return "", time.Time{}, kernel.NewError(kernel.CodeValidationFailed, "invites are for users")
	}
	tok := randomToken("fwi_", 24)
	exp := s.clock.Now().Add(InviteTTL)
	if err := s.store.CreateTicket(ctx, domain.Ticket{IDHash: HashToken(tok), PrincipalID: user, Purpose: domain.TicketOIDCInvite, ExpiresAt: exp}); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}
