package domain_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"

	"organon/internal/flow/domain"
)

func hmacOf(secret, data string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(data))
	return m.Sum(nil)
}

// ADR 0019: inbound webhooks verify the organon timestamped format (as sent
// by webhook.call), the older sha256= form, Stripe, and generic HMAC headers.
func TestVerifyWebhook(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"a":1}`)
	stamped := func(at time.Time, secret string) string {
		ts := strconv.FormatInt(at.Unix(), 10)
		return "t=" + ts + ",v1=" + hex.EncodeToString(hmacOf(secret, ts+"."+string(body)))
	}
	h := func(k, v string) http.Header { x := http.Header{}; x.Set(k, v); return x }
	cases := []struct {
		name string
		v    domain.Verify
		h    http.Header
		ok   bool
	}{
		{"organon timestamped", domain.Verify{}, h("X-Organon-Signature", stamped(now, "s")), true},
		{"organon wrong secret", domain.Verify{}, h("X-Organon-Signature", stamped(now, "x")), false},
		{"organon too old", domain.Verify{}, h("X-Organon-Signature", stamped(now.Add(-10*time.Minute), "s")), false},
		{"organon legacy sha256", domain.Verify{}, h("X-Organon-Signature", "sha256="+hex.EncodeToString(hmacOf("s", string(body)))), true},
		{"stripe", domain.Verify{Scheme: "stripe"}, h("Stripe-Signature", stamped(now, "s")+",v0=ignored"), true},
		{"stripe header missing", domain.Verify{Scheme: "stripe"}, h("X-Organon-Signature", stamped(now, "s")), false},
		{"hmac base64", domain.Verify{Scheme: "hmac-sha256", Header: "X-Shop-Hmac", Encoding: "base64"},
			h("X-Shop-Hmac", base64.StdEncoding.EncodeToString(hmacOf("s", string(body)))), true},
		{"hmac hex with prefix", domain.Verify{Scheme: "hmac-sha256", Header: "X-Hub-Signature-256", Prefix: "sha256="},
			h("X-Hub-Signature-256", "sha256="+hex.EncodeToString(hmacOf("s", string(body)))), true},
		{"hmac missing prefix", domain.Verify{Scheme: "hmac-sha256", Header: "X-Hub-Signature-256", Prefix: "sha256="},
			h("X-Hub-Signature-256", hex.EncodeToString(hmacOf("s", string(body)))), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := domain.VerifyWebhook(c.v, "s", body, c.h, now)
			if (err == nil) != c.ok {
				t.Fatalf("ok=%v, err=%v", c.ok, err)
			}
		})
	}
}

func TestHostAllowed(t *testing.T) {
	if !domain.HostAllowed("api.stripe.com", []string{"api.stripe.com"}) || !domain.HostAllowed("eu.api.example.com", []string{"*.example.com"}) {
		t.Fatal("exact and wildcard hosts match")
	}
	if domain.HostAllowed("api.stripe.com.evil.io", []string{"api.stripe.com"}) || domain.HostAllowed("example.com", []string{"*.example.com"}) {
		t.Fatal("suffix tricks and the bare domain do not match")
	}
}
