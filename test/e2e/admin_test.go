package e2e

import (
	"context"
	"strings"
	"testing"

	"organon/platform/config"
)

// ADR 0017: with admin accounts disabled, existing admins cannot sign in
// (password, sessions, API keys) while other users can; with the console
// disabled /admin is not served.
func TestAdminDisabled(t *testing.T) {
	dsn := t.TempDir() + "/app.db"
	useDB := func(c *config.Config) {
		c.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: dsn}}
	}
	s1 := startCfg(t, "sqlite", nil, useDB)
	admin := s1.admin()
	key := mustOK(t, admin.do("POST", "/v1/auth/tokens", map[string]any{"name": "ci"}), 201).str("key")
	s1.user(admin, "ana", "editor")
	if status, _, _ := s1.raw("GET", "/admin/", "", nil); status != 200 {
		t.Fatalf("console before: %d", status)
	}
	s1.rt.Close(context.Background())

	s2 := startCfg(t, "sqlite", nil, func(c *config.Config) {
		useDB(c)
		c.Admin.DisableAccounts, c.Admin.DisableConsole = true, true
	})
	r := (&client{s: s2}).do("POST", "/v1/auth/password", map[string]any{"login": adminEmail, "password": adminPassword, "cli": true})
	if r.Status != 403 || !strings.Contains(r.Raw, "admin accounts are disabled") {
		t.Fatalf("admin login: %d %s", r.Status, r.Raw)
	}
	for name, token := range map[string]string{"session": admin.token, "api key": key} {
		if r := (&client{s: s2, token: token}).do("GET", "/v1/auth/session", nil); r.Status != 401 {
			t.Fatalf("admin %s: %d %s", name, r.Status, r.Raw)
		}
	}
	ana := s2.login("ana@example.com", "password-ana-123")
	mustOK(t, ana.create("project", "", map[string]any{"title": "Still works"}), 201)
	for _, p := range []string{"/admin/", "/admin"} {
		if status, _, _ := s2.raw("GET", p, "", nil); status != 404 {
			t.Fatalf("console %s: %d", p, status)
		}
	}
}
