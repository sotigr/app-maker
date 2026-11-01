// Package jobs adapts kernel job requests to background execution (spec
// 2.5). Contexts register workers through a Registry at the composition
// root; domain and application code never import this package.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"app-maker/internal/kernel"
)

// Handler runs one job. It must be idempotent: jobs run at least once.
type Handler func(ctx context.Context, args json.RawMessage) error

// Registry maps job kinds to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	timeouts map[string]time.Duration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}, timeouts: map[string]time.Duration{}}
}

// Register adds a handler. timeout 0 means the default.
func (r *Registry) Register(kind string, h Handler, timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = h
	if timeout > 0 {
		r.timeouts[kind] = timeout
	}
}

// Lookup returns the handler and timeout for kind.
func (r *Registry) Lookup(kind string) (Handler, time.Duration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[kind]
	return h, r.timeouts[kind], ok
}

// Kinds returns the registered kinds, sorted.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks an error as not retryable; the job is discarded.
func Permanent(err error) error { return permanent{err} }

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// DefaultMaxAttempts is used when a request does not set MaxAttempts.
const DefaultMaxAttempts = 5

// Backoff returns the delay before attempt n (1-based retry number).
func Backoff(base time.Duration, n int) time.Duration {
	if n > 20 {
		n = 20
	}
	d := base << (n - 1)
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// ErrNoHandler is recorded for jobs whose kind has no handler.
var ErrNoHandler = fmt.Errorf("no handler registered")

// traced runs a job handler in a span continuing the trace of the request
// that enqueued it, and records job.duration (spec 14.3).
func traced(ctx context.Context, kind, queue string, carrier map[string]string, fn func(context.Context) error) error {
	ctx = kernel.Tel.Extract(ctx, carrier)
	ctx, end := kernel.Tel.StartSpan(ctx, "job "+kind, kernel.A("job.kind", kind), kernel.A("job.queue", queue))
	start := time.Now()
	err := fn(ctx)
	end(err)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	kernel.Tel.Record(ctx, "job.duration", time.Since(start).Seconds(),
		kernel.A("job.kind", kind), kernel.A("job.queue", queue), kernel.A("outcome", outcome))
	return err
}

// withTrace fills missing trace carriers from ctx.
func withTrace(ctx context.Context, jobs []kernel.JobRequest) []kernel.JobRequest {
	carrier := kernel.Tel.Inject(ctx)
	if carrier == nil {
		return jobs
	}
	out := make([]kernel.JobRequest, len(jobs))
	for i, j := range jobs {
		if j.Trace == nil {
			j.Trace = carrier
		}
		out[i] = j
	}
	return out
}
