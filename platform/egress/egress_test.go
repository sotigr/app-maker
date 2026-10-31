package egress

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Spec 14.2: private, loopback, link-local, and metadata destinations are
// refused, also after redirects.
func TestBlocksPrivateDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	c := New(Config{})
	for _, u := range []string{srv.URL, "http://169.254.169.254/latest/meta-data", "http://10.0.0.1/", "http://[::1]/", "ftp://example.com/"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if _, err := c.Do(req); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: want blocked, got %v", u, err)
		}
	}
	// Development may allow private addresses, but never metadata.
	dev := New(Config{AllowPrivate: true})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	res, err := dev.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, "http://169.254.169.254/", nil)
	if _, err := dev.Do(req); !errors.Is(err, ErrBlocked) {
		t.Errorf("metadata must stay blocked: %v", err)
	}
	allow := New(Config{AllowPrivate: true, Allow: []string{"api.example.com"}})
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := allow.Do(req); !errors.Is(err, ErrBlocked) {
		t.Errorf("allowlist not enforced: %v", err)
	}
}
