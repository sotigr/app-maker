package e2e

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"organon/internal/access/domain"
	manifestdomain "organon/internal/manifest/domain"
	"organon/platform/config"
)

func totpNow(t *testing.T, s *server, secret string) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return domain.TOTPCode(raw, domain.TOTPStep(s.clock.Now()))
}

// Spec 8.3: TOTP as a second step, required per role or opted into.
func TestMFA(t *testing.T) {
	s := startFull(t, "sqlite", nil, func(m *manifestdomain.Manifest) {
		m.App.Auth.MFA.RequiredForRoles = []string{"admin"}
	}, func(c *config.Config) {
		c.Secrets.DataKeyFile, c.Secrets.CreateDataKey = t.TempDir()+"/data.key", true
	})
	anon := s.anon()
	login := func(email, pw string) resp {
		return anon.do("POST", "/v1/auth/password", map[string]any{"login": email, "password": pw, "cli": true})
	}

	// The admin must enroll before getting a session.
	r := mustOK(t, login(adminEmail, adminPassword), 200)
	if r.str("mfa") != "enroll" || r.str("token") != "" {
		t.Fatalf("admin first login: %s", r.Raw)
	}
	e := mustOK(t, anon.do("POST", "/v1/auth/mfa/totp/enroll", map[string]any{"ticket": r.str("ticket")}), 200)
	if !strings.HasPrefix(e.str("uri"), "otpauth://totp/tracker:") {
		t.Fatalf("enroll: %s", e.Raw)
	}
	if r := anon.do("POST", "/v1/auth/mfa/totp/confirm", map[string]any{"ticket": e.str("ticket"), "code": "000000", "cli": true}); r.Status != 401 {
		t.Fatalf("wrong code: %s", r.Raw)
	}
	code := totpNow(t, s, e.str("secret"))
	ok := mustOK(t, anon.do("POST", "/v1/auth/mfa/totp/confirm", map[string]any{"ticket": e.str("ticket"), "code": code, "cli": true}), 200)
	admin := &client{s: s, token: ok.str("token"), id: ok.str("principal", "id")}
	mustOK(t, admin.do("GET", "/v1/auth/session", nil), 200)

	// Later logins ask for a code; the same code cannot be replayed.
	r = mustOK(t, login(adminEmail, adminPassword), 200)
	if r.str("mfa") != "verify" {
		t.Fatalf("second login: %s", r.Raw)
	}
	if r := anon.do("POST", "/v1/auth/mfa/verify", map[string]any{"ticket": r.str("ticket"), "code": code, "cli": true}); r.Status != 401 {
		t.Fatalf("replayed code: %s", r.Raw)
	}
	s.clock.Advance(30 * time.Second)
	mustOK(t, anon.do("POST", "/v1/auth/mfa/verify", map[string]any{"ticket": r.str("ticket"), "code": totpNow(t, s, e.str("secret")), "cli": true}), 200)
	if r := admin.do("POST", "/v1/auth/mfa/totp/disable", map[string]any{"code": "1"}); r.Status != 403 {
		t.Fatalf("disable required MFA: %s", r.Raw)
	}

	// Other users log in directly, and may opt in.
	u := s.user(admin, "uma")
	en := mustOK(t, u.do("POST", "/v1/auth/mfa/totp/enroll", map[string]any{}), 200)
	mustOK(t, u.do("POST", "/v1/auth/mfa/totp/confirm", map[string]any{"code": totpNow(t, s, en.str("secret"))}), 204)
	if r := login("uma@example.com", "password-uma-123"); r.str("mfa") != "verify" {
		t.Fatalf("opted-in login: %s", r.Raw)
	}
	s.clock.Advance(30 * time.Second)
	mustOK(t, u.do("POST", "/v1/auth/mfa/totp/disable", map[string]any{"code": totpNow(t, s, en.str("secret"))}), 204)
	if r := login("uma@example.com", "password-uma-123"); r.str("token") == "" {
		t.Fatalf("login after disabling: %s", r.Raw)
	}
}

// fakeIdP is a minimal OpenID provider for tests.
type fakeIdP struct {
	t     *testing.T
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]map[string]any // code -> claims + challenge
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{t: t, key: key, codes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		c, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || r.Form.Get("client_secret") != "client-secret" || base64.RawURLEncoding.EncodeToString(sum[:]) != c["challenge"] {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(c, "challenge")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(c), "token_type": "Bearer"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	b, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// browser follows the OIDC flow with a cookie jar, playing the user at
// the provider: it approves with the given subject and email.
type browser struct {
	t   *testing.T
	s   *server
	c   *http.Client
	idp *fakeIdP
}

func newBrowser(t *testing.T, s *server, idp *fakeIdP) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, s: s, idp: idp, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) get(u string) *http.Response {
	b.t.Helper()
	res, err := b.c.Get(u)
	if err != nil {
		b.t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode >= 400 {
		b.t.Logf("GET %s: %d %s", u, res.StatusCode, body)
	}
	return res
}

// login runs start → provider → callback and returns the callback response.
func (b *browser) login(startPath string, claims map[string]any, tamper bool) *http.Response {
	b.t.Helper()
	res := b.get(b.s.url + startPath)
	if res.StatusCode != http.StatusFound {
		b.t.Fatalf("start: %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "organon-test" {
		b.t.Fatalf("authorize request: %s", loc)
	}
	c := map[string]any{"iss": b.idp.srv.URL, "aud": "organon-test", "exp": b.s.clock.Now().Add(time.Hour).Unix(),
		"iat": b.s.clock.Now().Unix(), "nonce": q.Get("nonce"), "challenge": q.Get("code_challenge")}
	for k, v := range claims {
		c[k] = v
	}
	code := "code-" + q.Get("state")[:8]
	b.idp.mu.Lock()
	b.idp.codes[code] = c
	b.idp.mu.Unlock()
	state := q.Get("state")
	if tamper {
		state = "x" + state
	}
	return b.get(q.Get("redirect_uri") + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

func (b *browser) principal() string {
	b.t.Helper()
	res, err := b.c.Get(b.s.url + "/v1/auth/session")
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Principal struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"principal"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	return body.Principal.ID
}

// Spec 8.3: authorization code flow with PKCE; identities matched by
// provider and subject; unlinked identities need an invite unless the
// provider allows signup (decided 2026-11-01).
func TestOIDCLogin(t *testing.T) {
	idp := newFakeIdP(t)
	s := startFull(t, "memory", nil, func(m *manifestdomain.Manifest) {
		m.App.Auth.OIDC = []manifestdomain.OIDCProvider{
			{Provider: "test", Issuer: idp.srv.URL, ClientID: "organon-test", ClientSecret: "${secret:oidc-test}"},
			{Provider: "open", Issuer: idp.srv.URL, ClientID: "organon-test", ClientSecret: "${secret:oidc-test}", Signup: true, AllowedDomains: []string{"example.com"}},
		}
	}, nil)
	admin := s.admin()
	providers := mustOK(t, s.anon().do("GET", "/v1/auth/oidc", nil), 200)
	if !strings.Contains(providers.Raw, `"open"`) || !strings.Contains(providers.Raw, `"test"`) {
		t.Fatalf("providers: %s", providers.Raw)
	}

	// Unlinked identity, signup off: refused, even with a matching email.
	olga := s.user(admin, "olga")
	b := newBrowser(t, s, idp)
	if res := b.login("/v1/auth/oidc/test/start", map[string]any{"sub": "olga-sub", "email": "olga@example.com", "email_verified": true}, false); res.StatusCode != 401 {
		t.Fatalf("unlinked login: %d", res.StatusCode)
	}

	// An admin invite binds the identity on the first login.
	inv := mustOK(t, admin.do("POST", "/v1/auth/users/"+olga.id+"/oidc-invite", nil), 201)
	link := inv.Body["links"].(map[string]any)["test"].(string)
	if r := olga.do("POST", "/v1/auth/users/"+olga.id+"/oidc-invite", nil); r.Status != 403 {
		t.Fatalf("non-admin invite: %s", r.Raw)
	}
	res := b.login(link+"&redirect=/admin/", map[string]any{"sub": "olga-sub"}, false)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/admin/" {
		t.Fatalf("invited login: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if got := b.principal(); got != olga.id {
		t.Fatalf("session principal %q, want %q", got, olga.id)
	}
	// The invite is single-use; the linked identity logs in without it.
	b2 := newBrowser(t, s, idp)
	if res := b2.login(link, map[string]any{"sub": "other-sub"}, false); res.StatusCode != 401 {
		t.Fatalf("reused invite: %d", res.StatusCode)
	}
	b3 := newBrowser(t, s, idp)
	if res := b3.login("/v1/auth/oidc/test/start?redirect=https://evil.example/", map[string]any{"sub": "olga-sub"}, false); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("linked login: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if b3.principal() != olga.id {
		t.Fatal("linked login principal")
	}
	// Tampered state and forged tokens fail.
	if res := newBrowser(t, s, idp).login("/v1/auth/oidc/test/start", map[string]any{"sub": "olga-sub"}, true); res.StatusCode != 401 {
		t.Fatalf("tampered state: %d", res.StatusCode)
	}
	if res := newBrowser(t, s, idp).login("/v1/auth/oidc/test/start", map[string]any{"sub": "olga-sub", "aud": "someone-else"}, false); res.StatusCode != 401 {
		t.Fatalf("wrong audience: %d", res.StatusCode)
	}

	// Signup providers create users, limited to verified allowed domains.
	if res := newBrowser(t, s, idp).login("/v1/auth/oidc/open/start", map[string]any{"sub": "n1", "email": "n1@example.com", "email_verified": false}, false); res.StatusCode != 403 {
		t.Fatalf("unverified email signup: %d", res.StatusCode)
	}
	if res := newBrowser(t, s, idp).login("/v1/auth/oidc/open/start", map[string]any{"sub": "n2", "email": "n2@other.org", "email_verified": true}, false); res.StatusCode != 403 {
		t.Fatalf("foreign domain signup: %d", res.StatusCode)
	}
	nb := newBrowser(t, s, idp)
	if res := nb.login("/v1/auth/oidc/open/start", map[string]any{"sub": "n3", "email": "n3@example.com", "email_verified": true, "name": "New Person"}, false); res.StatusCode != http.StatusFound {
		t.Fatalf("signup: %d", res.StatusCode)
	}
	id := nb.principal()
	if id == "" {
		t.Fatal("no session after signup")
	}
	u := mustOK(t, admin.do("GET", "/v1/features/user/"+id, nil), 200)
	if u.str("data", "email") != "n3@example.com" {
		t.Fatalf("signed-up user: %s", u.Raw)
	}
}
