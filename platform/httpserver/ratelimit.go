package httpserver

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"organon/internal/kernel"
)

// RateLimiter admits requests per key at limit per minute (spec 14.1).
// The in-memory adapter serves the single topology; the replicated
// topology needs a shared adapter (Valkey).
type RateLimiter interface {
	Allow(key string, limit int) (ok bool, retryAfter time.Duration)
}

// MemoryLimiter is a GCRA limiter: requests are spread evenly over the
// minute with a burst of one full minute's allowance.
type MemoryLimiter struct {
	Now func() time.Time

	mu    sync.Mutex
	tat   map[string]time.Time // theoretical arrival time per key
	sweep time.Time
}

// NewMemoryLimiter returns an empty limiter.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{Now: time.Now, tat: map[string]time.Time{}}
}

// Allow implements RateLimiter.
func (m *MemoryLimiter) Allow(key string, limit int) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	now := m.Now()
	interval := time.Minute / time.Duration(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Sub(m.sweep) > time.Minute {
		for k, t := range m.tat {
			if t.Before(now) {
				delete(m.tat, k)
			}
		}
		m.sweep = now
	}
	tat := m.tat[key]
	if tat.Before(now) {
		tat = now
	}
	if wait := tat.Sub(now) - (time.Minute - interval); wait > 0 {
		return false, wait
	}
	m.tat[key] = tat.Add(interval)
	return true, 0
}

// RateConfig configures the rate limit middleware.
type RateConfig struct {
	PerPrincipal int // requests per minute per authenticated principal
	PerIP        int // requests per minute per IP for anonymous callers
	// Key returns the principal ID for authenticated requests ("" when
	// anonymous); it runs after authentication.
	Key func(r *http.Request) string
	// Exempt path prefixes (signed blob URLs carry their own bound).
	Exempt []string
	// TrustProxy uses the first X-Forwarded-For address as the client IP.
	TrustProxy bool
}

// RateLimit rejects requests over the limits with 429 and Retry-After.
func RateLimit(l RateLimiter, cfg RateConfig) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range cfg.Exempt {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			key, limit := "p:"+cfg.Key(r), cfg.PerPrincipal
			if key == "p:" {
				key, limit = "ip:"+clientIP(r, cfg.TrustProxy), cfg.PerIP
			}
			if ok, wait := l.Allow(key, limit); !ok {
				secs := int(wait/time.Second) + 1
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				WriteError(w, r, kernel.NewError(kernel.CodeRateLimited, "too many requests; retry in %d s", secs))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
