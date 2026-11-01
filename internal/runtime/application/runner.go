package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// Env is a prepared code bundle: its files on disk and the interpreter
// that runs them (a uv-built virtual environment or the system python3).
type Env struct {
	Dir    string
	Python string
}

// Environments prepares bundles once per hash (spec 11.5).
type Environments interface {
	Prepare(ctx context.Context, b rt.CodeBundle) (env Env, cached bool, err error)
}

// Frame is one runner↔worker message (length-prefixed JSON over stdio).
type Frame struct {
	T              string          `json:"t"`
	ID             uint64          `json:"id,omitempty"`
	Method         string          `json:"method,omitempty"`
	Args           json.RawMessage `json:"args,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *FrameError     `json:"error,omitempty"`
	Message        string          `json:"message,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	DeadlineUnixMs int64           `json:"deadlineUnixMs,omitempty"`
}

// FrameError is an error inside a frame.
type FrameError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Unhandled bool   `json:"unhandled,omitempty"`
}

// WorkerSpec describes a worker process to start.
type WorkerSpec struct {
	Env    Env
	Entry  string
	Limits rt.CodeLimits
}

// Worker is one isolated worker process. Kill must unblock Recv.
type Worker interface {
	Send(f Frame) error
	Recv() (Frame, error)
	Kill()
}

// WorkerStarter starts workers at the configured isolation level
// (spec 11.3: process, container, microvm).
type WorkerStarter interface {
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}

// RunnerConfig sizes worker pools (spec 11.4).
type RunnerConfig struct {
	MaxWorkers     int           // per pool; default 4
	MaxExecutions  int           // recycle after; default 500
	MaxAge         time.Duration // recycle after; default 30 minutes
	MaxOutputBytes int64         // default 1 MiB
}

// PoolStats reports one pool (spec 11.4).
type PoolStats struct {
	NodeID      string
	BundleHash  string
	Warm        int
	Busy        int
	Queued      int
	ColdStartMs int64
}

// Runner is the runner role: it keeps warm workers per (node, bundle) and
// relays host calls between workers and the engine.
type Runner struct {
	cfg     RunnerConfig
	envs    Environments
	starter WorkerStarter
	log     *slog.Logger

	mu      sync.Mutex
	bundles map[string]Env
	pools   map[poolKey]*pool
}

type poolKey struct {
	node, hash, entry, limits string
}

type pool struct {
	key    poolKey
	slots  chan struct{}
	mu     sync.Mutex
	idle   []*worker
	busy   int
	queued int
	coldMs int64
}

type worker struct {
	w       Worker
	started time.Time
	execs   int
}

// NewRunner returns a runner.
func NewRunner(cfg RunnerConfig, envs Environments, starter WorkerStarter, log *slog.Logger) *Runner {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 4
	}
	if cfg.MaxExecutions <= 0 {
		cfg.MaxExecutions = 500
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 30 * time.Minute
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 1 << 20
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{cfg: cfg, envs: envs, starter: starter, log: log, bundles: map[string]Env{}, pools: map[poolKey]*pool{}}
}

// ErrNotLoaded asks the engine to Load the bundle and retry.
var ErrNotLoaded = &rt.CodeError{Code: "not_loaded", Message: "the code bundle is not loaded on this runner", Retryable: true}

// Load prepares a bundle (spec 11.1 Load).
func (r *Runner) Load(ctx context.Context, b rt.CodeBundle) (bool, error) {
	r.mu.Lock()
	_, ok := r.bundles[b.Hash]
	r.mu.Unlock()
	if ok {
		return true, nil
	}
	env, cached, err := r.envs.Prepare(ctx, b)
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	r.bundles[b.Hash] = env
	r.mu.Unlock()
	return cached, nil
}

// Execute runs one node on a pooled worker; req.Bundle.Files may be empty
// (only the hash is used).
func (r *Runner) Execute(ctx context.Context, req rt.CodeRequest, host rt.CodeHost) (out json.RawMessage, err error) {
	ctx, end := kernel.Tel.StartSpan(ctx, "code "+req.NodeID, kernel.A("code.node", req.NodeID), kernel.A("code.run", req.RunID))
	defer func() { end(err) }()
	r.mu.Lock()
	env, ok := r.bundles[req.Bundle.Hash]
	r.mu.Unlock()
	if !ok {
		return nil, ErrNotLoaded
	}
	if !req.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, req.Deadline)
		defer cancel()
	}
	limits, _ := json.Marshal(req.Limits)
	p := r.pool(poolKey{node: req.NodeID, hash: req.Bundle.Hash, entry: req.Entry, limits: string(limits)})
	wk, err := p.acquire(ctx, func() (*worker, error) {
		return r.start(ctx, p, WorkerSpec{Env: env, Entry: req.Entry, Limits: req.Limits})
	})
	if err != nil {
		return nil, err
	}
	out, recycle, err := r.exec(ctx, wk, req, host)
	wk.execs++
	if recycle || wk.execs >= r.cfg.MaxExecutions || time.Since(wk.started) >= r.cfg.MaxAge {
		wk.w.Kill()
		wk = nil
	}
	p.release(wk)
	return out, err
}

func (r *Runner) exec(ctx context.Context, wk *worker, req rt.CodeRequest, host rt.CodeHost) (out json.RawMessage, recycle bool, err error) {
	var killed atomic.Bool
	stop := context.AfterFunc(ctx, func() { killed.Store(true); wk.w.Kill() })
	defer stop()
	f := Frame{T: "execute", Input: req.Input, IdempotencyKey: req.IdempotencyKey}
	if !req.Deadline.IsZero() {
		f.DeadlineUnixMs = req.Deadline.UnixMilli()
	}
	if len(f.Input) == 0 {
		f.Input = json.RawMessage("null")
	}
	crashed := func(err error) (json.RawMessage, bool, error) {
		if killed.Load() && ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		return nil, true, &rt.CodeError{Code: "worker_crashed", Message: err.Error(), Retryable: true}
	}
	if err := wk.w.Send(f); err != nil {
		return crashed(err)
	}
	for {
		msg, err := wk.w.Recv()
		if err != nil {
			return crashed(err)
		}
		switch msg.T {
		case "callback":
			res := Frame{T: "callbackResult", ID: msg.ID}
			v, herr := host(ctx, msg.Method, msg.Args)
			if herr != nil {
				res.Error = hostError(herr)
			} else {
				res.Result = v
			}
			if err := wk.w.Send(res); err != nil {
				return crashed(err)
			}
		case "result":
			if msg.Error != nil {
				return nil, msg.Error.Unhandled, &rt.CodeError{Code: msg.Error.Code, Message: msg.Error.Message, Unhandled: msg.Error.Unhandled}
			}
			limit := r.cfg.MaxOutputBytes
			if req.Limits.MaxOutputBytes > 0 && req.Limits.MaxOutputBytes < limit {
				limit = req.Limits.MaxOutputBytes
			}
			if int64(len(msg.Output)) > limit {
				return nil, false, &rt.CodeError{Code: "output_too_large", Message: fmt.Sprintf("output of %d bytes exceeds %d", len(msg.Output), limit)}
			}
			return msg.Output, false, nil
		default:
			return nil, true, &rt.CodeError{Code: "protocol", Message: "unexpected frame " + msg.T}
		}
	}
}

// hostError reports a failed host call to code; the code may catch it.
func hostError(err error) *FrameError {
	var ce interface{ ErrorCode() string }
	code := "host_error"
	if errors.As(err, &ce) {
		code = ce.ErrorCode()
	}
	return &FrameError{Code: code, Message: err.Error()}
}

func (r *Runner) start(ctx context.Context, p *pool, spec WorkerSpec) (*worker, error) {
	t0 := time.Now()
	w, err := r.starter.Start(ctx, spec)
	if err != nil {
		return nil, &rt.CodeError{Code: "worker_start", Message: err.Error(), Retryable: true}
	}
	ready, err := w.Recv()
	if err != nil || ready.T != "ready" {
		w.Kill()
		msg := ready.Message
		if err != nil {
			msg = err.Error()
		}
		return nil, &rt.CodeError{Code: "load_error", Message: msg}
	}
	ms := time.Since(t0).Milliseconds()
	kernel.Tel.Record(ctx, "codenode.cold_start_ms", float64(ms), kernel.A("node", p.key.node))
	p.mu.Lock()
	p.coldMs = ms
	p.mu.Unlock()
	r.log.Info("code worker started", "node", p.key.node, "bundle", short(p.key.hash), "coldStartMs", ms)
	return &worker{w: w, started: time.Now()}, nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (r *Runner) pool(k poolKey) *pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pools[k]
	if p == nil {
		p = &pool{key: k, slots: make(chan struct{}, r.cfg.MaxWorkers)}
		r.pools[k] = p
	}
	return p
}

func (p *pool) acquire(ctx context.Context, start func() (*worker, error)) (*worker, error) {
	p.mu.Lock()
	p.queued++
	p.mu.Unlock()
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		p.mu.Lock()
		p.queued--
		p.mu.Unlock()
		return nil, ctx.Err()
	}
	p.mu.Lock()
	p.queued--
	p.busy++
	var wk *worker
	if n := len(p.idle); n > 0 {
		wk, p.idle = p.idle[n-1], p.idle[:n-1]
	}
	p.mu.Unlock()
	if wk != nil {
		return wk, nil
	}
	wk, err := start()
	if err != nil {
		p.release(nil)
		return nil, err
	}
	return wk, nil
}

// release returns a worker (nil when it was killed) and frees the slot.
func (p *pool) release(wk *worker) {
	p.mu.Lock()
	p.busy--
	if wk != nil {
		p.idle = append(p.idle, wk)
	}
	p.mu.Unlock()
	<-p.slots
}

// Stats reports every pool.
func (r *Runner) Stats() []PoolStats {
	r.mu.Lock()
	pools := make([]*pool, 0, len(r.pools))
	for _, p := range r.pools {
		pools = append(pools, p)
	}
	r.mu.Unlock()
	out := make([]PoolStats, 0, len(pools))
	for _, p := range pools {
		p.mu.Lock()
		out = append(out, PoolStats{NodeID: p.key.node, BundleHash: p.key.hash, Warm: len(p.idle), Busy: p.busy, Queued: p.queued, ColdStartMs: p.coldMs})
		p.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Close kills every idle worker.
func (r *Runner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pools {
		p.mu.Lock()
		for _, wk := range p.idle {
			wk.w.Kill()
		}
		p.idle = nil
		p.mu.Unlock()
	}
}
