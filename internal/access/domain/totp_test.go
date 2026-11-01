package domain

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA-1, 8 digits truncated to 6).
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := TOTPCode(secret, TOTPStep(time.Unix(unix, 0))); got != want {
			t.Errorf("t=%d: %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_700_000_000, 0)
	step := TOTPStep(now)
	if s, ok := VerifyTOTP(secret, TOTPCode(secret, step-1), now, 0); !ok || s != step-1 {
		t.Fatal("previous step rejected")
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, step-2), now, 0); ok {
		t.Fatal("old code accepted")
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, step), now, step); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := VerifyTOTP(secret, "000000", now, 0); ok && TOTPCode(secret, step) != "000000" {
		t.Fatal("wrong code accepted")
	}
	uri := TOTPURI("tracker", "a@example.com", secret)
	if !strings.HasPrefix(uri, "otpauth://totp/tracker:a@example.com?") || !strings.Contains(uri, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ") {
		t.Fatalf("uri %s", uri)
	}
}
