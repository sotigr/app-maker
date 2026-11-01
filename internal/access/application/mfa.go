package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"slices"
	"time"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/access/domain"
	"app-maker/internal/kernel"
)

// TicketTTL bounds the time between the first and the second factor.
const TicketTTL = 10 * time.Minute

// ErrInvalidCode is the generic second-factor failure.
var ErrInvalidCode = kernel.NewError(kernel.CodeUnauthenticated, "invalid or expired code")

// mfaRequired reports whether p must use TOTP (spec 8.3: all users or
// per role).
func (s *AuthService) mfaRequired(p accessapi.Principal) bool {
	if p.Kind != accessapi.KindUser {
		return false
	}
	if s.cfg.MFAAll {
		return true
	}
	for _, r := range s.cfg.MFARoles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}

// finish completes a first factor: a session, or a ticket for the second
// factor (verify) or for enrollment when MFA is required (enroll).
func (s *AuthService) finish(ctx context.Context, p accessapi.Principal) (LoginResult, error) {
	t, err := s.store.TOTP(ctx, p.ID)
	switch {
	case err == nil && t.Confirmed:
		return s.pending(ctx, p, domain.TicketMFAVerify, s.clock.Now().Add(TicketTTL))
	case err != nil && !errors.Is(err, kernel.ErrNotFound):
		return LoginResult{}, err
	case s.mfaRequired(p):
		return s.pending(ctx, p, domain.TicketMFAEnroll, s.clock.Now().Add(TicketTTL))
	}
	return s.openSession(ctx, p)
}

func (s *AuthService) pending(ctx context.Context, p accessapi.Principal, purpose string, exp time.Time) (LoginResult, error) {
	tok := randomToken("fwt_", 24)
	if err := s.store.CreateTicket(ctx, domain.Ticket{IDHash: HashToken(tok), PrincipalID: p.ID, Purpose: purpose, ExpiresAt: exp}); err != nil {
		return LoginResult{}, err
	}
	mfa := "verify"
	if purpose == domain.TicketMFAEnroll {
		mfa = "enroll"
	}
	return LoginResult{Principal: p, MFA: mfa, Ticket: tok}, nil
}

// take consumes a ticket of the given purpose.
func (s *AuthService) take(ctx context.Context, ticket, purpose string) (domain.Ticket, error) {
	t, err := s.store.TakeTicket(ctx, HashToken(ticket))
	if err != nil || t.Purpose != purpose || s.clock.Now().After(t.ExpiresAt) {
		return domain.Ticket{}, ErrInvalidCode
	}
	return t, nil
}

// retry puts a ticket back after a wrong code while attempts remain.
func (s *AuthService) retry(ctx context.Context, t domain.Ticket) {
	key := "mfa:" + t.PrincipalID.String()
	s.limiter.Fail(key)
	if s.limiter.Allow(key) {
		_ = s.store.CreateTicket(ctx, t)
	}
}

// checkCode verifies a TOTP code and records its step against replay.
func (s *AuthService) checkCode(ctx context.Context, t domain.TOTP, code string) bool {
	if s.cfg.Sealer == nil {
		return false
	}
	secret, err := s.cfg.Sealer.Open(t.Secret)
	if err != nil {
		return false
	}
	step, ok := domain.VerifyTOTP(secret, code, s.clock.Now(), t.LastStep)
	if !ok {
		return false
	}
	t.LastStep, t.Confirmed = step, true
	return s.store.PutTOTP(ctx, t) == nil
}

// VerifyMFA completes a login with a TOTP code.
func (s *AuthService) VerifyMFA(ctx context.Context, ticket, code string) (LoginResult, error) {
	t, err := s.take(ctx, ticket, domain.TicketMFAVerify)
	if err != nil {
		return LoginResult{}, err
	}
	if !s.limiter.Allow("mfa:" + t.PrincipalID.String()) {
		return LoginResult{}, kernel.NewError(kernel.CodeRateLimited, "too many attempts")
	}
	totp, err := s.store.TOTP(ctx, t.PrincipalID)
	if err != nil || !totp.Confirmed || !s.checkCode(ctx, totp, code) {
		s.retry(ctx, t)
		s.audit(ctx, t.PrincipalID, "mfa_failed", "totp")
		return LoginResult{}, ErrInvalidCode
	}
	s.audit(ctx, t.PrincipalID, "mfa_verified", "totp")
	s.limiter.Reset("mfa:" + t.PrincipalID.String())
	p, err := s.Resolve(ctx, t.PrincipalID)
	if err != nil {
		return LoginResult{}, ErrInvalidCode
	}
	return s.openSession(ctx, p)
}

// Enrollment is a new, unconfirmed TOTP secret.
type Enrollment struct {
	Secret string `json:"secret"` // base32, for manual entry
	URI    string `json:"uri"`    // otpauth:// for QR codes
	Ticket string `json:"ticket,omitempty"`
}

// BeginTOTP creates an unconfirmed secret for p; an existing confirmed
// one must be disabled first.
func (s *AuthService) BeginTOTP(ctx context.Context, principal kernel.ID) (Enrollment, error) {
	if s.cfg.Sealer == nil {
		return Enrollment{}, kernel.NewError(kernel.CodeUnsupportedCapability, "MFA needs an instance data key (secrets.dataKeyFile)")
	}
	if t, err := s.store.TOTP(ctx, principal); err == nil && t.Confirmed {
		return Enrollment{}, kernel.NewError(kernel.CodeConflict, "TOTP is already enabled")
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return Enrollment{}, err
	}
	sealed, err := s.cfg.Sealer.Seal(secret)
	if err != nil {
		return Enrollment{}, err
	}
	if err := s.store.PutTOTP(ctx, domain.TOTP{PrincipalID: principal, Secret: sealed}); err != nil {
		return Enrollment{}, err
	}
	account := principal.String()
	if f, err := s.reader.Get(ctx, principal); err == nil {
		if e, ok := f.Data["email"].(string); ok && e != "" {
			account = e
		}
	}
	issuer := s.cfg.Issuer
	if issuer == "" {
		issuer = "app"
	}
	uri := domain.TOTPURI(issuer, account, secret)
	return Enrollment{Secret: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), URI: uri}, nil
}

// BeginTOTPWithTicket enrolls during a login that requires MFA.
func (s *AuthService) BeginTOTPWithTicket(ctx context.Context, ticket string) (Enrollment, error) {
	t, err := s.take(ctx, ticket, domain.TicketMFAEnroll)
	if err != nil {
		return Enrollment{}, err
	}
	e, err := s.BeginTOTP(ctx, t.PrincipalID)
	if err != nil {
		return Enrollment{}, err
	}
	next := randomToken("fwt_", 24)
	if err := s.store.CreateTicket(ctx, domain.Ticket{IDHash: HashToken(next), PrincipalID: t.PrincipalID, Purpose: domain.TicketMFAEnroll, ExpiresAt: t.ExpiresAt}); err != nil {
		return Enrollment{}, err
	}
	e.Ticket = next
	return e, nil
}

// ConfirmTOTP activates an enrollment with a first code.
func (s *AuthService) ConfirmTOTP(ctx context.Context, principal kernel.ID, code string) error {
	t, err := s.store.TOTP(ctx, principal)
	if err != nil || t.Confirmed {
		return kernel.NewError(kernel.CodeConflict, "no TOTP enrollment is pending")
	}
	if !s.checkCode(ctx, t, code) {
		return ErrInvalidCode
	}
	s.audit(ctx, principal, "totp_enabled", "")
	return nil
}

// ConfirmTOTPWithTicket confirms an enrollment during login and opens the
// session.
func (s *AuthService) ConfirmTOTPWithTicket(ctx context.Context, ticket, code string) (LoginResult, error) {
	t, err := s.take(ctx, ticket, domain.TicketMFAEnroll)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.ConfirmTOTP(ctx, t.PrincipalID, code); err != nil {
		s.retry(ctx, t)
		return LoginResult{}, err
	}
	p, err := s.Resolve(ctx, t.PrincipalID)
	if err != nil {
		return LoginResult{}, ErrInvalidCode
	}
	return s.openSession(ctx, p)
}

// DisableTOTP removes TOTP after a valid code, unless MFA is required.
func (s *AuthService) DisableTOTP(ctx context.Context, p accessapi.Principal, code string) error {
	if s.mfaRequired(p) {
		return kernel.NewError(kernel.CodeForbidden, "MFA is required for this account")
	}
	t, err := s.store.TOTP(ctx, p.ID)
	if err != nil {
		return kernel.NewError(kernel.CodeConflict, "TOTP is not enabled")
	}
	if t.Confirmed && !s.checkCode(ctx, t, code) {
		return ErrInvalidCode
	}
	s.audit(ctx, p.ID, "totp_disabled", "")
	return s.store.DeleteTOTP(ctx, p.ID)
}
