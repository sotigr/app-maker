package e2e

import (
	"strings"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
	"organon/platform/config"
)

// ADR 0027: account operations of the admin console. Admins only (or the
// principal itself where noted), audited, and no secret ever returned.
func TestAdminAccountOperations(t *testing.T) {
	s := startFull(t, "sqlite", nil, func(m *manifestdomain.Manifest) {}, func(c *config.Config) {
		c.Secrets.DataKeyFile, c.Secrets.CreateDataKey = t.TempDir()+"/data.key", true
	})
	admin := s.admin()
	anon := s.anon()
	ben := s.user(admin, "ben", "editor")
	other := s.user(admin, "olive")
	login := func(email, pw string) resp {
		return anon.do("POST", "/v1/auth/password", map[string]any{"login": email, "password": pw, "cli": true})
	}

	// The sign-in setup the console offers.
	meta := mustOK(t, admin.do("GET", "/v1/admin/meta", nil), 200)
	if !strings.Contains(meta.Raw, `"name":"editor"`) || !strings.Contains(meta.Raw, `"minPasswordLength":12`) {
		t.Fatalf("meta: %s", meta.Raw)
	}
	if r := ben.do("GET", "/v1/admin/meta", nil); r.Status != 403 {
		t.Fatalf("meta as non-admin: %d", r.Status)
	}

	// A user invited through OIDC has no password until an admin sets one.
	if r := admin.do("POST", "/v1/auth/users", map[string]any{"email": "ivy@example.com", "password": "password-ivy-123", "invite": true}); r.Status != 400 {
		t.Fatalf("invite with a password: %d %s", r.Status, r.Raw)
	}
	if r := admin.do("POST", "/v1/auth/users", map[string]any{"email": "ivy@example.com", "password": "short"}); r.Status != 400 || !strings.Contains(r.Raw, "/password") {
		t.Fatalf("short password: %d %s", r.Status, r.Raw)
	}
	ivy := mustOK(t, admin.do("POST", "/v1/auth/users", map[string]any{"email": "ivy@example.com", "displayName": "Ivy", "invite": true}), 201).str("id")
	if c := mustOK(t, admin.do("GET", "/v1/auth/users/"+ivy+"/credentials", nil), 200); !strings.Contains(c.Raw, `"password":false`) {
		t.Fatalf("invited user credentials: %s", c.Raw)
	}
	if r := admin.do("POST", "/v1/auth/users/"+ivy+"/password", map[string]any{"password": "short"}); r.Status != 400 {
		t.Fatalf("short new password: %d", r.Status)
	}
	if r := ben.do("POST", "/v1/auth/users/"+ivy+"/password", map[string]any{"password": "password-ivy-456"}); r.Status != 403 {
		t.Fatalf("set password as non-admin: %d", r.Status)
	}
	mustOK(t, admin.do("POST", "/v1/auth/users/"+ivy+"/password", map[string]any{"password": "password-ivy-456"}), 204)
	mustOK(t, login("ivy@example.com", "password-ivy-456"), 200)

	// A new password signs the user out everywhere; the old one stops working.
	mustOK(t, ben.do("GET", "/v1/auth/session", nil), 200)
	mustOK(t, admin.do("POST", "/v1/auth/users/"+ben.id+"/password", map[string]any{"password": "password-ben-456"}), 204)
	if r := ben.do("GET", "/v1/auth/session", nil); r.Status != 401 {
		t.Fatalf("old session after a new password: %d", r.Status)
	}
	if r := login("ben@example.com", "password-ben-123"); r.Status != 401 {
		t.Fatalf("old password: %d", r.Status)
	}
	ben = s.login("ben@example.com", "password-ben-456")

	// API keys: listed without secrets, revoked at once.
	key := mustOK(t, admin.do("POST", "/v1/auth/tokens", map[string]any{"principal": ben.id, "name": "ci"}), 201).str("key")
	keyClient := &client{s: s, token: key}
	mustOK(t, keyClient.do("GET", "/v1/auth/session", nil), 200)
	list := mustOK(t, admin.do("GET", "/v1/auth/tokens?principal="+ben.id, nil), 200)
	items, _ := list.Body["items"].([]any)
	if len(items) != 1 || strings.Contains(list.Raw, key) || strings.Contains(list.Raw, "hash") || !strings.Contains(list.Raw, `"name":"ci"`) {
		t.Fatalf("key list: %s", list.Raw)
	}
	prefix := items[0].(map[string]any)["prefix"].(string)
	mustOK(t, ben.do("GET", "/v1/auth/tokens", nil), 200) // own keys
	if r := other.do("GET", "/v1/auth/tokens?principal="+ben.id, nil); r.Status != 403 {
		t.Fatalf("another user's keys: %d", r.Status)
	}
	if r := other.do("DELETE", "/v1/auth/tokens/"+prefix+"?principal="+ben.id, nil); r.Status != 403 {
		t.Fatalf("revoke another user's key: %d", r.Status)
	}
	mustOK(t, admin.do("DELETE", "/v1/auth/tokens/"+prefix+"?principal="+ben.id, nil), 204)
	if r := keyClient.do("GET", "/v1/auth/session", nil); r.Status != 401 {
		t.Fatalf("revoked key still works: %d", r.Status)
	}
	if r := admin.do("DELETE", "/v1/auth/tokens/"+prefix+"?principal="+ben.id, nil); r.Status != 404 {
		t.Fatalf("revoke twice: %d", r.Status)
	}

	// MFA reset: the next sign-in needs no code.
	en := mustOK(t, ben.do("POST", "/v1/auth/mfa/totp/enroll", map[string]any{}), 200)
	mustOK(t, ben.do("POST", "/v1/auth/mfa/totp/confirm", map[string]any{"code": totpNow(t, s, en.str("secret"))}), 204)
	if c := mustOK(t, admin.do("GET", "/v1/auth/users/"+ben.id+"/credentials", nil), 200); !strings.Contains(c.Raw, `"mfa":true`) || !strings.Contains(c.Raw, `"login":"ben@example.com"`) {
		t.Fatalf("credentials with MFA: %s", c.Raw)
	}
	if r := login("ben@example.com", "password-ben-456"); r.str("mfa") != "verify" {
		t.Fatalf("login with MFA: %s", r.Raw)
	}
	if r := other.do("DELETE", "/v1/auth/users/"+ben.id+"/mfa", nil); r.Status != 403 {
		t.Fatalf("MFA reset as non-admin: %d", r.Status)
	}
	mustOK(t, admin.do("DELETE", "/v1/auth/users/"+ben.id+"/mfa", nil), 204)
	if r := login("ben@example.com", "password-ben-456"); r.str("token") == "" {
		t.Fatalf("login after MFA reset: %s", r.Raw)
	}

	// Sign out everywhere: admins, or the user for themselves.
	ben = s.login("ben@example.com", "password-ben-456")
	if r := other.do("DELETE", "/v1/auth/users/"+ben.id+"/sessions", nil); r.Status != 403 {
		t.Fatalf("sign out another user: %d", r.Status)
	}
	mustOK(t, admin.do("DELETE", "/v1/auth/users/"+ben.id+"/sessions", nil), 204)
	if r := ben.do("GET", "/v1/auth/session", nil); r.Status != 401 {
		t.Fatalf("session after sign-out everywhere: %d", r.Status)
	}

	// Every operation is audited.
	audit := mustOK(t, admin.do("GET", "/v1/audit?limit=200", nil), 200)
	for _, action := range []string{"user_created", "password_set", "api_key_revoked", "mfa_reset", "sessions_revoked"} {
		if !strings.Contains(audit.Raw, action) {
			t.Errorf("audit misses %s", action)
		}
	}
}
