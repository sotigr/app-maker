package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"organon/internal/kernel"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first listed is outermost.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type requestIDKey struct{}

// RequestID returns the request's ID.
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// WithRequestID assigns each request an ID (X-Request-Id).
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// Recover turns panics into 500 problem responses.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", v, "path", r.URL.Path)
				WriteError(w, r, kernel.NewError(kernel.CodeInternal, "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// LimitBody caps request bodies (spec 14.1: 1 MiB). PUT requests under
// the exempt prefixes (signed blob uploads) are limited by their handler.
func LimitBody(n int64, exempt ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog logs each request without field values (spec 14.3).
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			log.InfoContext(r.Context(), "http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"ms", time.Since(start).Milliseconds(), "request_id", RequestID(r.Context()))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.body != nil {
		s.body.Write(b)
	}
	return s.ResponseWriter.Write(b)
}

// Flush supports streaming responses through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// StoredResponse is a response kept for idempotent replay.
type StoredResponse struct {
	Status  int
	Header  http.Header
	Body    []byte
	Expires time.Time
}

// IdempotencyStore keeps responses by key for 24 hours (spec 6.1).
type IdempotencyStore interface {
	Get(ctx context.Context, key string) (StoredResponse, bool, error)
	Put(ctx context.Context, key string, r StoredResponse) error
}

// MemoryIdempotency is an in-memory store (single topology only).
type MemoryIdempotency struct {
	mu sync.Mutex
	m  map[string]StoredResponse
}

// NewMemoryIdempotency returns an empty store.
func NewMemoryIdempotency() *MemoryIdempotency {
	return &MemoryIdempotency{m: map[string]StoredResponse{}}
}

// Get implements IdempotencyStore.
func (s *MemoryIdempotency) Get(_ context.Context, key string) (StoredResponse, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[key]
	if ok && time.Now().After(r.Expires) {
		delete(s.m, key)
		return r, false, nil
	}
	return r, ok, nil
}

// Put implements IdempotencyStore.
func (s *MemoryIdempotency) Put(_ context.Context, key string, r StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = r
	return nil
}

// Idempotency replays stored responses for repeated Idempotency-Key
// headers on mutating requests. scope returns a caller identity so keys
// of different principals never collide.
func Idempotency(store IdempotencyStore, scope func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			full := scope(r) + "|" + r.Method + "|" + r.URL.Path + "|" + key
			if stored, ok, err := store.Get(r.Context(), full); err == nil && ok {
				for k, v := range stored.Header {
					w.Header()[k] = v
				}
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(stored.Status)
				_, _ = w.Write(stored.Body)
				return
			}
			rec := &statusRecorder{ResponseWriter: w, status: 200, body: &bytes.Buffer{}}
			next.ServeHTTP(rec, r)
			if rec.status < 500 {
				_ = store.Put(r.Context(), full, StoredResponse{Status: rec.status, Header: w.Header().Clone(),
					Body: rec.body.Bytes(), Expires: time.Now().Add(24 * time.Hour)})
			}
		})
	}
}
