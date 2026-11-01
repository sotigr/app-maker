package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	accessapp "app-maker/internal/access/application"
	accesscrypto "app-maker/internal/access/infrastructure/crypto"
	"app-maker/internal/access/infrastructure/oidc"
	"app-maker/internal/kernel"
	manifestdomain "app-maker/internal/manifest/domain"
	"app-maker/platform/config"
)

// wellKnownIssuers lets app.json name common providers without an issuer.
var wellKnownIssuers = map[string]string{
	"google": "https://accounts.google.com",
	"gitlab": "https://gitlab.com",
}

var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// checkAuth validates app.json auth.oidc and auth.mfa (fw validate).
func checkAuth(a manifestdomain.AuthConfig, roles map[string][]string) []Problem {
	var probs []Problem
	seen := map[string]bool{}
	for i, p := range a.OIDC {
		ptr := fmt.Sprintf("/auth/oidc/%d", i)
		add := func(field, msg string) {
			probs = append(probs, Problem{File: "app.json", Pointer: ptr + field, Message: msg})
		}
		switch {
		case !providerName.MatchString(p.Provider):
			add("/provider", "provider names are lower-case letters, digits, and dashes")
		case seen[p.Provider]:
			add("/provider", "duplicate provider")
		}
		seen[p.Provider] = true
		if p.ClientID == "" {
			add("/clientId", "clientId is required")
		}
		if !strings.HasPrefix(p.ClientSecret, "${secret:") {
			add("/clientSecret", "clientSecret must be a ${secret:<name>} reference")
		}
		issuer := p.Issuer
		if issuer == "" {
			issuer = wellKnownIssuers[p.Provider]
		}
		if issuer == "" {
			add("/issuer", "issuer is required for provider "+p.Provider)
		} else if !strings.HasPrefix(issuer, "https://") && !strings.HasPrefix(issuer, "http://localhost") && !strings.HasPrefix(issuer, "http://127.0.0.1") {
			add("/issuer", "issuer must use https")
		}
	}
	for i, r := range a.MFA.RequiredForRoles {
		if _, ok := roles[r]; !ok {
			probs = append(probs, Problem{File: "app.json", Pointer: fmt.Sprintf("/auth/mfa/requiredForRoles/%d", i), Message: "unknown role " + r})
		}
	}
	return probs
}

// authConfig builds the authentication settings: MFA roles, the data key
// sealer for TOTP secrets, the OIDC state sealer, and OIDC providers whose
// calls go through the egress client.
func authConfig(cfg config.Config, c *Compiled, instanceKey []byte, secrets func(string) (string, bool), out oidc.HTTPDoer, clock kernel.Clock) (accessapp.AuthConfig, error) {
	a := c.Manifest.App.Auth
	ac := accessapp.AuthConfig{PasswordEnabled: a.Password.Enabled, MinPasswordLength: a.Password.MinLength,
		MFAAll: a.MFA.RequiredForAll, MFARoles: slices.Clone(a.MFA.RequiredForRoles), Issuer: c.Manifest.App.App,
		OIDC: map[string]accessapp.OIDCConfig{}}
	key, err := dataKey(cfg)
	if err != nil {
		return ac, err
	}
	if key != nil {
		if ac.Sealer, err = accesscrypto.NewSealer(key); err != nil {
			return ac, fmt.Errorf("secrets.dataKeyFile: %w", err)
		}
	}
	m := hmac.New(sha256.New, instanceKey)
	m.Write([]byte("oidc-state"))
	if ac.StateSealer, err = accesscrypto.NewSealer(m.Sum(nil)); err != nil {
		return ac, err
	}
	for _, p := range a.OIDC {
		secret, ok := secrets(p.ClientSecret)
		if !ok {
			return ac, fmt.Errorf("auth.oidc %s: secret %s is not set", p.Provider, p.ClientSecret)
		}
		issuer := p.Issuer
		if issuer == "" {
			issuer = wellKnownIssuers[p.Provider]
		}
		client := oidc.New(oidc.Config{Issuer: issuer, ClientID: p.ClientID, ClientSecret: secret, Scopes: p.Scopes}, out)
		client.Now = clock.Now
		ac.OIDC[p.Provider] = accessapp.OIDCConfig{Signup: p.Signup, AllowedDomains: p.AllowedDomains, Provider: client}
	}
	return ac, nil
}

// dataKey loads the instance data key (spec 8.2, 14.7): hex in a key file
// outside the database. Memory credential stores get a per-process key;
// without a key file, TOTP enrollment is unavailable.
func dataKey(cfg config.Config) ([]byte, error) {
	path := cfg.Secrets.DataKeyFile
	if path == "" {
		if cfg.Credentials.Driver == "memory" {
			k := make([]byte, 32)
			_, err := rand.Read(k)
			return k, err
		}
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && cfg.Secrets.CreateDataKey {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets.dataKeyFile: %w", err)
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("secrets.dataKeyFile: want 64 hex characters (32 bytes)")
	}
	return k, nil
}
