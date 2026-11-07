// Package e2e runs black-box tests against a wired server over HTTP
// (spec 15.7), using the tracker example app.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"organon/internal/app"
	integrationinfra "organon/internal/integration/infrastructure"
	"organon/internal/kernel"
	manifestdomain "organon/internal/manifest/domain"
	manifestfs "organon/internal/manifest/infrastructure/fs"
	"organon/platform/config"
)

const adminEmail, adminPassword = "admin@example.com", "correct-horse-battery"

type server struct {
	t      *testing.T
	url    string
	rt     *app.Runtime
	clock  *kernel.FakeClock
	emails *integrationinfra.Recorder
}

// testSecrets resolves the secrets used by tracker flows in tests.
func testSecrets(ref string) (string, bool) {
	switch ref {
	case "${secret:issues-webhook}":
		return "s3cret", true
	case "${secret:oidc-test}":
		return "client-secret", true
	case "${secret:pay}", "${secret:mydata-key}":
		return "sk_live_SECRETVALUE", true
	case "${secret:mydata-user}":
		return "user-SECRETVALUE", true
	}
	return "", false
}

// eachDriver runs fn against the memory and the SQLite (+River) backends.
func eachDriver(t *testing.T, fn func(t *testing.T, driver string)) {
	for _, d := range []string{"memory", "sqlite"} {
		t.Run(d, func(t *testing.T) { fn(t, d) })
	}
}

func start(t *testing.T, driver string) *server { return startWith(t, driver, nil) }

// startWith starts the tracker plus extra flow files (test-only flows).
func startWith(t *testing.T, driver string, extraFlows map[string]string) *server {
	t.Helper()
	return startCfg(t, driver, extraFlows, nil)
}

// startCfg starts the tracker with a configuration change.
func startCfg(t *testing.T, driver string, extraFlows map[string]string, change func(*config.Config)) *server {
	t.Helper()
	return startFull(t, driver, extraFlows, nil, change)
}

// startFull starts the tracker with manifest and configuration changes.
func startFull(t *testing.T, driver string, extraFlows map[string]string, edit func(*manifestdomain.Manifest), change func(*config.Config)) *server {
	t.Helper()
	m, err := manifestfs.Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	for name, src := range extraFlows {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/" + name, Data: []byte(src)})
	}
	compiled, err := app.Compile(m)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.LLM.Provider = "fake"
	cfg.Blobs.Dir = t.TempDir()
	cfg.Manifest = "../../examples/tracker"
	cfg.Dev = true
	cfg.Bootstrap.AdminEmail, cfg.Bootstrap.AdminPassword = adminEmail, adminPassword
	if driver == "sqlite" {
		cfg.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: t.TempDir() + "/app.db"}}
		cfg.Credentials.Driver = "sqlite"
	}
	if change != nil {
		change(&cfg)
	}
	clock := kernel.NewFakeClock(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	emails := &integrationinfra.Recorder{}
	rt, err := app.Build(ctx, cfg, compiled, app.Options{Clock: clock, FastHashing: true,
		Log: slog.New(slog.DiscardHandler), JobBackoff: time.Millisecond, Email: emails, Secrets: testSecrets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(rt.Handler)
	t.Cleanup(ts.Close)
	return &server{t: t, url: ts.URL, rt: rt, clock: clock, emails: emails}
}

type client struct {
	s     *server
	token string
	id    string
}

type resp struct {
	Status int
	Header http.Header
	Raw    string
	Body   map[string]any
}

func (r resp) str(path ...string) string {
	var cur any = r.Body
	for _, p := range path {
		m, _ := cur.(map[string]any)
		cur = m[p]
	}
	s, _ := cur.(string)
	return s
}

func (r resp) code() string { return r.str("code") }

func (c *client) do(method, path string, body any, headers ...string) resp {
	c.s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.s.url+path, rd)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (s *server) anon() *client { return &client{s: s} }

func (s *server) login(email, password string) *client {
	s.t.Helper()
	c := &client{s: s}
	r := c.do("POST", "/v1/auth/password", map[string]any{"login": email, "password": password, "cli": true})
	if r.Status != 200 {
		s.t.Fatalf("login %s: %d %s", email, r.Status, r.Raw)
	}
	c.token = r.str("token")
	c.id = r.str("principal", "id")
	return c
}

func (s *server) admin() *client { return s.login(adminEmail, adminPassword) }

// user creates a user (as admin) and logs in.
func (s *server) user(admin *client, name string, roles ...string) *client {
	s.t.Helper()
	email := name + "@example.com"
	r := admin.do("POST", "/v1/auth/users", map[string]any{"email": email, "displayName": name, "password": "password-" + name + "-123", "roles": roles})
	if r.Status != 201 {
		s.t.Fatalf("create user %s: %d %s", name, r.Status, r.Raw)
	}
	return s.login(email, "password-"+name+"-123")
}

func (c *client) create(typ, parent string, data map[string]any) resp {
	c.s.t.Helper()
	path := "/v1/features/" + typ
	if parent != "" {
		path += "?parent=" + parent
	}
	return c.do("POST", path, data)
}

func mustOK(t *testing.T, r resp, status int) resp {
	t.Helper()
	if r.Status != status {
		t.Fatalf("want %d, got %d: %s", status, r.Status, r.Raw)
	}
	return r
}
