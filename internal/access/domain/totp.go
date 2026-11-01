package domain

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP uses HMAC-SHA1 (authenticator apps)
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// TOTP parameters (RFC 6238 defaults understood by authenticator apps).
const (
	TOTPPeriod = 30 * time.Second
	TOTPDigits = 6
	// TOTPSkew accepts codes one step before or after the current one.
	TOTPSkew = 1
)

// TOTPStep returns the time step of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

// TOTPCode returns the code of a step.
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, v%1_000_000)
}

// VerifyTOTP checks a code at now within the skew; codes of steps at or
// before lastStep are refused (replay). It returns the matched step.
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	cur := TOTPStep(now)
	for d := -int64(TOTPSkew); d <= TOTPSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// TOTPURI returns the otpauth:// URI for authenticator apps.
func TOTPURI(issuer, account string, secret []byte) string {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {enc}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(TOTPDigits)},
		"period": {fmt.Sprint(int(TOTPPeriod / time.Second))}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// SecretSealer encrypts secrets at rest with the instance data key
// (spec 8.2, 14.7).
type SecretSealer interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}
