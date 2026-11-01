package e2e

import (
	"strings"
	"testing"

	"organon/platform/config"
)

// Spec 14.1: requests per principal and per IP, streaming connections per
// principal, and document shape limits.
func TestRequestLimits(t *testing.T) {
	s := startCfg(t, "memory", nil, func(c *config.Config) {
		c.Limits.RequestsPerMinute = 1000
		c.Limits.AnonymousPerMinute = 3
	})
	admin := s.admin() // one anonymous request (login)
	anon := s.anon()
	anon.do("GET", "/v1/schema", nil)
	anon.do("GET", "/v1/schema", nil)
	r := anon.do("GET", "/v1/schema", nil)
	if r.Status != 429 || r.code() != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("anonymous limit: %d %s", r.Status, r.Raw)
	}
	// Authenticated callers have their own allowance.
	mustOK(t, admin.do("GET", "/v1/schema", nil), 200)

	// app.json lowers requestsPerMinute to 600; the operator's 1000 does not raise it.
	got := 0
	for i := 0; i < 700; i++ {
		if admin.do("GET", "/v1/auth/session", nil).Status == 429 {
			break
		}
		got++
	}
	if got < 590 || got > 600 {
		t.Fatalf("principal allowance: %d requests before 429", got)
	}
}

func TestStreamingAndShapeLimits(t *testing.T) {
	w := setup(t, "memory")
	for i := 0; i < 20; i++ {
		w.editor.ws(t)
	}
	r := w.editor.do("GET", "/v1/stream?target=feature&id="+w.project, nil)
	if r.Status != 429 {
		t.Fatalf("21st streaming connection: %d %s", r.Status, r.Raw)
	}
	tags := make([]string, 1001)
	for i := range tags {
		tags[i] = "t"
	}
	r = w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": strings.Repeat("x", 3), "tags": tags}, "If-Match", w.taskVersion(t, w.task))
	if r.Status != 400 || !strings.Contains(r.Raw, "maxItems") {
		t.Fatalf("1001 list items: %d %s", r.Status, r.Raw)
	}
}
