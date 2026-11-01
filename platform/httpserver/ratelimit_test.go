package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Spec 14.1: requests per principal and per IP.
func TestMemoryLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewMemoryLimiter()
	l.Now = func() time.Time { return now }
	for i := 0; i < 60; i++ {
		if ok, _ := l.Allow("a", 60); !ok {
			t.Fatalf("request %d rejected within the burst", i)
		}
	}
	ok, wait := l.Allow("a", 60)
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("over the limit: %v %v", ok, wait)
	}
	if ok, _ := l.Allow("b", 60); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(time.Second) // one request's worth of time
	if ok, _ := l.Allow("a", 60); !ok {
		t.Fatal("not refilled")
	}
	if ok, _ := l.Allow("a", 60); ok {
		t.Fatal("refilled too much")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	l := NewMemoryLimiter()
	h := RateLimit(l, RateConfig{PerPrincipal: 3, PerIP: 1, Exempt: []string{"/v1/blobs/"},
		Key: func(r *http.Request) string { return r.Header.Get("X-P") }})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	do := func(path, principal string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if principal != "" {
			r.Header.Set("X-P", principal)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if do("/x", "").Code != 200 || do("/x", "").Code != 429 {
		t.Fatal("anonymous per-IP limit")
	}
	for i := 0; i < 3; i++ {
		if c := do("/x", "u1").Code; c != 200 {
			t.Fatalf("principal request %d: %d", i, c)
		}
	}
	w := do("/x", "u1")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("principal limit: %d %v", w.Code, w.Header())
	}
	if do("/v1/blobs/x", "").Code != 200 {
		t.Fatal("exempt path limited")
	}
}
