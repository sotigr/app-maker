// Package oidc is an OpenID Connect relying party (spec 8.3): discovery,
// authorization code flow with PKCE, and ID token verification against
// the provider's JWKS (RS256, ES256). All calls go through the egress
// client (spec 14.2).
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"app-maker/internal/access/domain"
)

// HTTPDoer performs outbound requests (the egress client).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Config describes one provider.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Client is one provider.
type Client struct {
	cfg  Config
	http HTTPDoer
	Now  func() time.Time

	mu     sync.Mutex
	disc   *discovery
	keys   map[string]crypto.PublicKey
	keysAt time.Time
}

var _ domain.OIDCProvider = (*Client)(nil)

type discovery struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

// New returns a client; discovery happens on first use.
func New(cfg Config, h HTTPDoer) *Client {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	return &Client{cfg: cfg, http: h, Now: time.Now}
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

func (c *Client) discover(ctx context.Context) (*discovery, error) {
	c.mu.Lock()
	d := c.disc
	c.mu.Unlock()
	if d != nil {
		return d, nil
	}
	var nd discovery
	if err := c.getJSON(ctx, strings.TrimSuffix(c.cfg.Issuer, "/")+"/.well-known/openid-configuration", &nd); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if strings.TrimSuffix(nd.Issuer, "/") != strings.TrimSuffix(c.cfg.Issuer, "/") {
		return nil, fmt.Errorf("oidc discovery: issuer %q does not match %q", nd.Issuer, c.cfg.Issuer)
	}
	c.mu.Lock()
	c.disc = &nd
	c.mu.Unlock()
	return &nd, nil
}

// AuthURL implements domain.OIDCProvider.
func (c *Client) AuthURL(ctx context.Context, state, nonce, challenge, redirectURI string) (string, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{"response_type": {"code"}, "client_id": {c.cfg.ClientID}, "redirect_uri": {redirectURI},
		"scope": {strings.Join(c.cfg.Scopes, " ")}, "state": {state}, "nonce": {nonce},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	sep := "?"
	if strings.Contains(d.AuthURL, "?") {
		sep = "&"
	}
	return d.AuthURL + sep + q.Encode(), nil
}

// Exchange implements domain.OIDCProvider.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI, nonce string) (domain.ExternalClaims, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return domain.ExternalClaims{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {c.cfg.ClientID}, "client_secret": {c.cfg.ClientSecret}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return domain.ExternalClaims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return domain.ExternalClaims{}, fmt.Errorf("oidc token: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil {
		return domain.ExternalClaims{}, fmt.Errorf("oidc token: %w", err)
	}
	if res.StatusCode != http.StatusOK || tok.IDToken == "" {
		return domain.ExternalClaims{}, fmt.Errorf("oidc token: HTTP %d %s", res.StatusCode, tok.Error)
	}
	return c.Verify(ctx, tok.IDToken, nonce)
}

type idClaims struct {
	Iss           string          `json:"iss"`
	Sub           string          `json:"sub"`
	Aud           json.RawMessage `json:"aud"`
	Azp           string          `json:"azp"`
	Exp           int64           `json:"exp"`
	Iat           int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"`
	Name          string          `json:"name"`
}

// Verify checks an ID token's signature and claims.
func (c *Client) Verify(ctx context.Context, token, nonce string) (domain.ExternalClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return domain.ExternalClaims{}, errors.New("id token: malformed")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSeg(parts[0], &hdr); err != nil {
		return domain.ExternalClaims{}, fmt.Errorf("id token header: %w", err)
	}
	key, err := c.key(ctx, hdr.Kid)
	if err != nil {
		return domain.ExternalClaims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return domain.ExternalClaims{}, errors.New("id token: bad signature encoding")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch k := key.(type) {
	case *rsa.PublicKey:
		if hdr.Alg != "RS256" || rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig) != nil {
			return domain.ExternalClaims{}, errors.New("id token: invalid signature")
		}
	case *ecdsa.PublicKey:
		if hdr.Alg != "ES256" || len(sig) != 64 ||
			!ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return domain.ExternalClaims{}, errors.New("id token: invalid signature")
		}
	default:
		return domain.ExternalClaims{}, errors.New("id token: unsupported key")
	}
	var cl idClaims
	if err := decodeSeg(parts[1], &cl); err != nil {
		return domain.ExternalClaims{}, fmt.Errorf("id token claims: %w", err)
	}
	now := c.Now().Unix()
	switch {
	case strings.TrimSuffix(cl.Iss, "/") != strings.TrimSuffix(c.cfg.Issuer, "/"):
		return domain.ExternalClaims{}, errors.New("id token: wrong issuer")
	case !audience(cl.Aud, c.cfg.ClientID) || (cl.Azp != "" && cl.Azp != c.cfg.ClientID):
		return domain.ExternalClaims{}, errors.New("id token: wrong audience")
	case cl.Exp+60 < now:
		return domain.ExternalClaims{}, errors.New("id token: expired")
	case cl.Iat-60 > now:
		return domain.ExternalClaims{}, errors.New("id token: issued in the future")
	case nonce == "" || cl.Nonce != nonce:
		return domain.ExternalClaims{}, errors.New("id token: nonce mismatch")
	case cl.Sub == "":
		return domain.ExternalClaims{}, errors.New("id token: no subject")
	}
	verified := cl.EmailVerified == true || cl.EmailVerified == "true"
	return domain.ExternalClaims{Subject: cl.Sub, Email: strings.ToLower(cl.Email), EmailVerified: verified, Name: cl.Name}, nil
}

func audience(raw json.RawMessage, client string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == client
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == client {
				return true
			}
		}
	}
	return false
}

func decodeSeg(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// key returns the signing key; unknown key IDs refresh the JWKS at most
// once a minute (key rotation).
func (c *Client) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	c.mu.Lock()
	k, ok := c.keys[kid]
	stale := c.Now().Sub(c.keysAt) > time.Minute
	c.mu.Unlock()
	if ok {
		return k, nil
	}
	if !stale && c.keys != nil {
		return nil, fmt.Errorf("id token: unknown key %q", kid)
	}
	d, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, d.JWKSURL, &set); err != nil {
		return nil, fmt.Errorf("oidc jwks: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		switch j.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(j.N)
			e, err2 := base64.RawURLEncoding.DecodeString(j.E)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[j.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			if j.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(j.X)
			y, err2 := base64.RawURLEncoding.DecodeString(j.Y)
			if err1 != nil || err2 != nil || len(x) > 32 || len(y) > 32 {
				continue
			}
			point := make([]byte, 65)
			point[0] = 4
			copy(point[33-len(x):33], x)
			copy(point[65-len(y):], y)
			pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
			if err != nil {
				continue
			}
			keys[j.Kid] = pk
		}
	}
	c.mu.Lock()
	c.keys, c.keysAt = keys, c.Now()
	c.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("id token: unknown key %q", kid)
}
