package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Verify configures how an inbound webhook is authenticated (ADR 0019).
type Verify struct {
	// Scheme is organon (default), stripe, or hmac-sha256.
	Scheme string `json:"scheme"`
	// Header, Prefix, and Encoding (hex or base64) configure hmac-sha256:
	// the header holds Prefix + HMAC-SHA256(body).
	Header   string `json:"header,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

// Webhook signature schemes.
const (
	VerifyOrganon = "organon"
	VerifyStripe  = "stripe"
	VerifyHMAC    = "hmac-sha256"
)

// SignatureTolerance is how old a timestamped signature may be.
const SignatureTolerance = 5 * time.Minute

// ErrBadSignature reports a missing, wrong, or expired signature.
var ErrBadSignature = errors.New("invalid webhook signature")

// VerifyWebhook checks the signature of an inbound webhook body.
//
// organon: X-Organon-Signature "t=<unix>,v1=<hex HMAC(t + "." + body)>",
// the format outbound webhook.call deliveries use; the older
// "sha256=<hex HMAC(body)>" is still accepted.
// stripe: Stripe-Signature, the same timestamped scheme.
// hmac-sha256: a configured header with Prefix + HMAC(body).
func VerifyWebhook(v Verify, secret string, body []byte, h http.Header, now time.Time) error {
	switch v.Scheme {
	case "", VerifyOrganon:
		sig := h.Get("X-Organon-Signature")
		if hexSig, ok := strings.CutPrefix(sig, "sha256="); ok {
			return equal(hexSig, hex.EncodeToString(mac(secret, body)))
		}
		return timestamped(sig, secret, body, now)
	case VerifyStripe:
		return timestamped(h.Get("Stripe-Signature"), secret, body, now)
	case VerifyHMAC:
		sig, ok := strings.CutPrefix(h.Get(v.Header), v.Prefix)
		if !ok {
			return ErrBadSignature
		}
		want := hex.EncodeToString(mac(secret, body))
		if v.Encoding == "base64" {
			want = base64.StdEncoding.EncodeToString(mac(secret, body))
		}
		return equal(sig, want)
	}
	return ErrBadSignature
}

// timestamped checks "t=<unix>,v1=<hex>[,v1=...]" signatures.
func timestamped(sig, secret string, body []byte, now time.Time) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(sig, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = val
		case "v1":
			sigs = append(sigs, val)
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(unix, 0)); d > SignatureTolerance || d < -SignatureTolerance {
		return ErrBadSignature
	}
	want := hex.EncodeToString(mac(secret, append([]byte(ts+"."), body...)))
	for _, s := range sigs {
		if equal(s, want) == nil {
			return nil
		}
	}
	return ErrBadSignature
}

func mac(secret string, data []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(data)
	return m.Sum(nil)
}

func equal(got, want string) error {
	if !hmac.Equal([]byte(got), []byte(want)) {
		return ErrBadSignature
	}
	return nil
}
