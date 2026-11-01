package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ES256 tokens and the claim checks (issuer, audience, expiry, nonce).
func TestVerifyES256(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks",
			"authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "EC", "crv": "P-256", "kid": "e1",
			"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
			"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32)))}}})
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	c := New(Config{Issuer: srv.URL, ClientID: "cid"}, http.DefaultClient)
	c.Now = func() time.Time { return now }
	sign := func(claims map[string]any) string {
		h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "e1"})
		b, _ := json.Marshal(claims)
		in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
		sum := sha256.Sum256([]byte(in))
		r, s, _ := ecdsa.Sign(rand.Reader, key, sum[:])
		sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		return in + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	base := func() map[string]any {
		return map[string]any{"iss": srv.URL, "sub": "s", "aud": []string{"cid", "other"}, "azp": "cid",
			"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "n", "email": "A@Example.com", "email_verified": "true"}
	}
	got, err := c.Verify(context.Background(), sign(base()), "n")
	if err != nil || got.Subject != "s" || got.Email != "a@example.com" || !got.EmailVerified {
		t.Fatalf("valid token: %+v %v", got, err)
	}
	for name, change := range map[string]func(map[string]any){
		"issuer":   func(m map[string]any) { m["iss"] = "https://evil" },
		"audience": func(m map[string]any) { m["aud"] = "x" },
		"azp":      func(m map[string]any) { m["azp"] = "x" },
		"expired":  func(m map[string]any) { m["exp"] = now.Add(-time.Hour).Unix() },
		"nonce":    func(m map[string]any) { m["nonce"] = "m" },
		"subject":  func(m map[string]any) { delete(m, "sub") },
	} {
		m := base()
		change(m)
		if _, err := c.Verify(context.Background(), sign(m), "n"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	tok := sign(base())
	if _, err := c.Verify(context.Background(), tok[:len(tok)-4]+"AAAA", "n"); err == nil {
		t.Error("bad signature accepted")
	}
}
