// Package health serves the startup, liveness, and readiness probes on the
// internal port (spec 14.10). Contexts register checks at the composition
// root.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Check is one dependency check.
type Check struct {
	Name string
	// Required checks fail readiness; optional ones report "degraded".
	Required bool
	Fn       func(ctx context.Context) error
}

// Registry collects checks and serves the probes.
type Registry struct {
	mu        sync.Mutex
	checks    []Check
	started   atomic.Bool
	stopping  atomic.Bool
	lastTick  atomic.Int64
	cacheAt   time.Time
	cacheBody []byte
	cacheOK   bool
}

// NewRegistry returns a registry with its watchdog tick set to now.
func NewRegistry() *Registry {
	r := &Registry{}
	r.lastTick.Store(time.Now().UnixNano())
	return r
}

// Register adds a check.
func (r *Registry) Register(c Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, c)
}

// MarkStarted reports startup done (configuration, tables, manifest).
func (r *Registry) MarkStarted() { r.started.Store(true) }

// MarkStopping fails readiness immediately (spec 14.10 shutdown).
func (r *Registry) MarkStopping() { r.stopping.Store(true) }

// RunWatchdog ticks until ctx ends; liveness fails when it stops ticking.
func (r *Registry) RunWatchdog(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.lastTick.Store(now.UnixNano())
		}
	}
}

type result struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Latency int64  `json:"latencyMs"`
	Error   string `json:"error,omitempty"`
}

// Handler returns the probe mux: /startupz, /livez, /readyz.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /startupz", func(w http.ResponseWriter, _ *http.Request) {
		if !r.started.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		if time.Since(time.Unix(0, r.lastTick.Load())) > 10*time.Second {
			http.Error(w, "watchdog stalled", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, req *http.Request) {
		body, ok := r.ready(req.Context())
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write(body)
	})
	return mux
}

// ready runs the checks, cached for 2 s so probes never load dependencies.
func (r *Registry) ready(ctx context.Context) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping.Load() || !r.started.Load() {
		b, _ := json.Marshal(map[string]any{"status": "unavailable", "stopping": r.stopping.Load(), "started": r.started.Load()})
		return b, false
	}
	if time.Since(r.cacheAt) < 2*time.Second && r.cacheBody != nil {
		return r.cacheBody, r.cacheOK
	}
	ok := true
	results := make([]result, 0, len(r.checks))
	for _, c := range r.checks {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		start := time.Now()
		err := c.Fn(cctx)
		cancel()
		res := result{Name: c.Name, Status: "ok", Latency: time.Since(start).Milliseconds()}
		if err != nil {
			res.Error = err.Error()
			if c.Required {
				res.Status, ok = "failed", false
			} else {
				res.Status = "degraded"
			}
		}
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	status := "ok"
	if !ok {
		status = "unavailable"
	}
	r.cacheBody, _ = json.Marshal(map[string]any{"status": status, "checks": results})
	r.cacheOK, r.cacheAt = ok, time.Now()
	return r.cacheBody, ok
}
