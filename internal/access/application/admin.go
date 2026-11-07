package application

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/access/domain"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// Account operations of the admin console (ADR 0027). Every operation
// checks the actor and is audited; secrets (password hashes, key hashes,
// TOTP secrets) never leave the service.

// RoleInfo is one app role and the roles it includes.
type RoleInfo struct {
	Name     string   `json:"name"`
	Includes []string `json:"includes"`
}

// AdminMeta is the sign-in setup the console needs to offer the right
// choices: roles, OIDC providers, the password policy, and MFA rules.
type AdminMeta struct {
	Roles             []RoleInfo `json:"roles"`
	OIDC              []string   `json:"oidc"`
	PasswordEnabled   bool       `json:"passwordEnabled"`
	MinPasswordLength int        `json:"minPasswordLength"`
	MFAAll            bool       `json:"mfaAll"`
	MFARoles          []string   `json:"mfaRoles"`
	AdminsDisabled    bool       `json:"adminsDisabled"`
}

// Credentials summarizes how a principal signs in; no secrets.
type Credentials struct {
	Password bool     `json:"password"`
	Login    string   `json:"login,omitempty"`
	MFA      bool     `json:"mfa"`
	OIDC     []string `json:"oidc"`
	Keys     int      `json:"keys"`
}

// KeyInfo describes an API key without its secret.
type KeyInfo struct {
	Prefix    string    `json:"prefix"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func isAdmin(actor accessapi.Principal) bool { return actor.IsSystem() || actor.HasRole("admin") }

// AdminMeta returns the sign-in setup; admins only.
func (s *AuthService) AdminMeta(actor accessapi.Principal) (AdminMeta, error) {
	if !isAdmin(actor) {
		return AdminMeta{}, kernel.ErrForbidden
	}
	m := AdminMeta{OIDC: s.OIDCProviders(), PasswordEnabled: s.cfg.PasswordEnabled, MinPasswordLength: s.cfg.MinPasswordLength,
		MFAAll: s.cfg.MFAAll, MFARoles: append([]string{}, s.cfg.MFARoles...), AdminsDisabled: s.cfg.NoAdmins}
	for name, inc := range s.cfg.Roles {
		m.Roles = append(m.Roles, RoleInfo{Name: name, Includes: append([]string{}, inc...)})
	}
	if _, ok := s.cfg.Roles["admin"]; !ok && !s.cfg.NoAdmins {
		m.Roles = append(m.Roles, RoleInfo{Name: "admin", Includes: []string{}})
	}
	sort.Slice(m.Roles, func(i, j int) bool { return m.Roles[i].Name < m.Roles[j].Name })
	if m.OIDC == nil {
		m.OIDC = []string{}
	}
	return m, nil
}

// principalOf loads a user or service account.
func (s *AuthService) principalOf(ctx context.Context, id kernel.ID) (map[string]any, schemaapi.TypeID, error) {
	f, err := s.reader.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	base := f.Type.Base()
	if base != schemaapi.TypeUser && base != schemaapi.TypeServiceAccount {
		return nil, "", kernel.NewError(kernel.CodeBadRequest, "not a user or service account")
	}
	return f.Data, base, nil
}

// mayManageKeys is CreateAPIKey's rule: admins, the principal itself, and
// the owner of a service account.
func (s *AuthService) mayManageKeys(ctx context.Context, actor accessapi.Principal, principal kernel.ID) error {
	data, _, err := s.principalOf(ctx, principal)
	if err != nil {
		return err
	}
	owner, _ := data["owner"].(string)
	if !isAdmin(actor) && owner != actor.ID.String() && principal != actor.ID {
		return kernel.ErrForbidden
	}
	return nil
}

// Credentials reports a principal's sign-in methods; admins and the
// principal itself.
func (s *AuthService) Credentials(ctx context.Context, actor accessapi.Principal, id kernel.ID) (Credentials, error) {
	if !isAdmin(actor) && actor.ID != id {
		return Credentials{}, kernel.ErrForbidden
	}
	if _, _, err := s.principalOf(ctx, id); err != nil {
		return Credentials{}, err
	}
	c := Credentials{OIDC: []string{}}
	if pw, err := s.store.PasswordOf(ctx, id); err == nil {
		c.Password, c.Login = true, pw.Login
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return Credentials{}, err
	}
	if t, err := s.store.TOTP(ctx, id); err == nil {
		c.MFA = t.Confirmed
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return Credentials{}, err
	}
	ids, err := s.store.IdentitiesOf(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	for _, i := range ids {
		if !slices.Contains(c.OIDC, i.Provider) {
			c.OIDC = append(c.OIDC, i.Provider)
		}
	}
	keys, err := s.store.APIKeysOf(ctx, id)
	if err != nil {
		return Credentials{}, err
	}
	c.Keys = len(keys)
	return c, nil
}

// APIKeys lists a principal's keys without their secrets.
func (s *AuthService) APIKeys(ctx context.Context, actor accessapi.Principal, principal kernel.ID) ([]KeyInfo, error) {
	if err := s.mayManageKeys(ctx, actor, principal); err != nil {
		return nil, err
	}
	keys, err := s.store.APIKeysOf(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := make([]KeyInfo, len(keys))
	for i, k := range keys {
		out[i] = KeyInfo{Prefix: k.Prefix, Name: k.Name, CreatedAt: k.CreatedAt}
	}
	return out, nil
}

// RevokeAPIKey deletes one key; it stops working at once.
func (s *AuthService) RevokeAPIKey(ctx context.Context, actor accessapi.Principal, principal kernel.ID, prefix string) error {
	if err := s.mayManageKeys(ctx, actor, principal); err != nil {
		return err
	}
	if err := s.store.DeleteAPIKey(ctx, principal, prefix); err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "api_key_revoked", "for "+principal.String()+" prefix "+prefix)
	return nil
}

// SetPassword gives a user a new password (creating the credential for a
// user who had none, e.g. one invited through OIDC) and signs them out
// everywhere. Admins only.
func (s *AuthService) SetPassword(ctx context.Context, actor accessapi.Principal, id kernel.ID, password string) error {
	if !isAdmin(actor) {
		return kernel.ErrForbidden
	}
	data, base, err := s.principalOf(ctx, id)
	if err != nil {
		return err
	}
	if base != schemaapi.TypeUser {
		return kernel.NewError(kernel.CodeBadRequest, "only users have passwords; service accounts use API keys")
	}
	if len(password) < s.cfg.MinPasswordLength {
		return kernel.NewValidationError(kernel.FieldError{Path: "/password", Code: "minLength", Message: "password is too short"})
	}
	login := ""
	if pw, err := s.store.PasswordOf(ctx, id); err == nil {
		login = pw.Login
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	if login == "" {
		email, _ := data["email"].(string)
		if email == "" {
			return kernel.NewValidationError(kernel.FieldError{Path: "/email", Code: "required", Message: "the user needs an email to sign in with"})
		}
		login = normalizeLogin(email)
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return err
	}
	if err := s.store.PutPassword(ctx, domain.PasswordCredential{PrincipalID: id, Login: login, Hash: hash}); err != nil {
		return err
	}
	if err := s.store.DeleteSessionsOf(ctx, id); err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "password_set", "for "+id.String())
	return nil
}

// ResetMFA removes a user's authenticator; when MFA is required they
// enroll again at their next sign-in. Admins only.
func (s *AuthService) ResetMFA(ctx context.Context, actor accessapi.Principal, id kernel.ID) error {
	if !isAdmin(actor) {
		return kernel.ErrForbidden
	}
	if _, _, err := s.principalOf(ctx, id); err != nil {
		return err
	}
	if err := s.store.DeleteTOTP(ctx, id); err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	s.audit(ctx, actor.ID, "mfa_reset", "for "+id.String())
	return nil
}

// SignOutEverywhere ends every session of a principal; admins and the
// principal itself.
func (s *AuthService) SignOutEverywhere(ctx context.Context, actor accessapi.Principal, id kernel.ID) error {
	if !isAdmin(actor) && actor.ID != id {
		return kernel.ErrForbidden
	}
	if _, _, err := s.principalOf(ctx, id); err != nil {
		return err
	}
	if err := s.store.DeleteSessionsOf(ctx, id); err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "sessions_revoked", "for "+id.String())
	return nil
}
