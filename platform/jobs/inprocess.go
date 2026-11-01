package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"app-maker/internal/kernel"
)

// InProcessConfig configures the in-process adapter.
type InProcessConfig struct {
	// Concurrency per queue; queues not listed get 1 worker.
	Concurrency map[string]int
	// BaseBackoff is the first retry delay (default 1s).
	BaseBackoff time.Duration
	// DefaultTimeout bounds jobs whose kind sets no timeout (default 60s).
	DefaultTimeout time.Duration
}

// Discarded is a job that exhausted its attempts.
type Discarded struct {
	Request  kernel.JobRequest
	Attempts int
	Err      string
	At       time.Time
}

// InProcess runs jobs in goroutines after commit. It is not durable and is
// refused in the replicated topology (spec 2.5). It implements
// kernel.JobEnqueuer.
type InProcess struct {
	reg *Registry
	cfg InProcessConfig
	log *slog.Logger

	mu        sync.Mutex
	cond      *sync.Cond
	pending   map[string][]*pendingJob // by queue
	unique    map[string]bool          // unique keys of pending jobs
	inflight  int
	discarded []Discarded
	completed int
	started   bool
	wake      map[string]chan struct{}
	periodic  []periodic
}

type periodic struct {
	name string
	next func(time.Time) time.Time
	job  kernel.JobRequest
}

// AddPeriodic registers a periodic job; it starts with Start. In-process
// scheduling suits the single topology only.
func (p *InProcess) AddPeriodic(name string, next func(time.Time) time.Time, job kernel.JobRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.periodic = append(p.periodic, periodic{name: name, next: next, job: job})
	return nil
}

func (p *InProcess) runPeriodic(ctx context.Context, pj periodic) {
	for {
		wait := time.Until(pj.next(time.Now()))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			_ = p.Enqueue(ctx, []kernel.JobRequest{pj.job})
		}
	}
}

type pendingJob struct {
	req     kernel.JobRequest
	attempt int
	runAt   time.Time
}

var _ kernel.JobEnqueuer = (*InProcess)(nil)

// NewInProcess returns an adapter; call Start to run workers.
func NewInProcess(reg *Registry, cfg InProcessConfig, log *slog.Logger) *InProcess {
	if cfg.BaseBackoff == 0 {
		cfg.BaseBackoff = time.Second
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 60 * time.Second
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p := &InProcess{reg: reg, cfg: cfg, log: log, pending: map[string][]*pendingJob{},
		unique: map[string]bool{}, wake: map[string]chan struct{}{}}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Enqueue adds jobs. Jobs with a UniqueKey equal to a pending job's key are
// dropped (spec 2.5: bursts coalesce).
func (p *InProcess) Enqueue(ctx context.Context, jobs []kernel.JobRequest) error {
	jobs = withTrace(ctx, jobs)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, r := range jobs {
		if r.UniqueKey != "" {
			if p.unique[r.UniqueKey] {
				continue
			}
			p.unique[r.UniqueKey] = true
		}
		q := r.Queue
		if q == "" {
			q = kernel.QueueMaintenance
		}
		runAt := now
		if r.ScheduledAt != nil {
			runAt = *r.ScheduledAt
		}
		p.pending[q] = append(p.pending[q], &pendingJob{req: r, attempt: 0, runAt: runAt})
		p.signal(q)
	}
	p.cond.Broadcast()
	return nil
}

func (p *InProcess) signal(q string) {
	ch, ok := p.wake[q]
	if !ok {
		ch = make(chan struct{}, 1)
		p.wake[q] = ch
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Start runs workers for the configured queues until ctx is cancelled.
func (p *InProcess) Start(ctx context.Context) {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	queues := map[string]int{kernel.QueueEvents: 1, kernel.QueueFlows: 1, kernel.QueueEffects: 1, kernel.QueueMaintenance: 1}
	for q, n := range p.cfg.Concurrency {
		queues[q] = n
	}
	for q := range queues {
		p.signal(q)
	}
	periodics := append([]periodic(nil), p.periodic...)
	p.mu.Unlock()
	for _, pj := range periodics {
		go p.runPeriodic(ctx, pj)
	}
	for q, n := range queues {
		for i := 0; i < n; i++ {
			go p.worker(ctx, q)
		}
	}
	go func() {
		<-ctx.Done()
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	}()
}

func (p *InProcess) worker(ctx context.Context, q string) {
	p.mu.Lock()
	wake := p.wake[q]
	p.mu.Unlock()
	for {
		j, wait := p.take(q)
		if j == nil {
			var timer *time.Timer
			var fire <-chan time.Time
			if wait > 0 {
				timer = time.NewTimer(wait)
				fire = timer.C
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
				p.signalOthers(q, wake)
			case <-fire:
			}
			if timer != nil {
				timer.Stop()
			}
			continue
		}
		p.run(ctx, q, j)
	}
}

// signalOthers re-arms the wake channel so sibling workers also look.
func (p *InProcess) signalOthers(q string, wake chan struct{}) {
	p.mu.Lock()
	if len(p.pending[q]) > 1 {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}

// take removes the next ready job of queue q, or returns the delay until
// the earliest scheduled one.
func (p *InProcess) take(q string) (*pendingJob, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	list := p.pending[q]
	best := -1
	var wait time.Duration
	for i, j := range list {
		if !j.runAt.After(now) {
			best = i
			break
		}
		if d := j.runAt.Sub(now); wait == 0 || d < wait {
			wait = d
		}
	}
	if best < 0 {
		return nil, wait
	}
	j := list[best]
	p.pending[q] = append(list[:best:best], list[best+1:]...)
	if j.req.UniqueKey != "" {
		delete(p.unique, j.req.UniqueKey)
	}
	p.inflight++
	return j, 0
}

func (p *InProcess) run(ctx context.Context, q string, j *pendingJob) {
	j.attempt++
	h, timeout, ok := p.reg.Lookup(j.req.Kind)
	var err error
	if !ok {
		err = Permanent(ErrNoHandler)
	} else {
		if timeout == 0 {
			timeout = p.cfg.DefaultTimeout
		}
		jctx, cancel := context.WithTimeout(ctx, timeout)
		max := j.req.MaxAttempts
		if max <= 0 {
			max = DefaultMaxAttempts
		}
		jctx = kernel.WithJobAttempt(jctx, kernel.JobAttempt{Attempt: j.attempt, Max: max})
		err = safeRun(jctx, h, j.req)
		cancel()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight--
	defer p.cond.Broadcast()
	if err == nil {
		p.completed++
		return
	}
	max := j.req.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	if IsPermanent(err) || j.attempt >= max || ctx.Err() != nil {
		p.log.Warn("job discarded", "kind", j.req.Kind, "attempts", j.attempt, "err", err)
		p.discarded = append(p.discarded, Discarded{Request: j.req, Attempts: j.attempt, Err: err.Error(), At: time.Now()})
		return
	}
	j.runAt = time.Now().Add(Backoff(p.cfg.BaseBackoff, j.attempt))
	p.pending[q] = append(p.pending[q], j)
	p.signal(q)
}

func safeRun(ctx context.Context, h Handler, req kernel.JobRequest) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(errPanic{r})
		}
	}()
	return traced(ctx, req.Kind, req.Queue, req.Trace, func(ctx context.Context) error { return h(ctx, req.Args) })
}

type errPanic struct{ v any }

func (e errPanic) Error() string { return "job panicked" }

// Drain blocks until no job is pending or running, or ctx ends. Jobs
// scheduled in the future count as pending.
func (p *InProcess) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		p.mu.Lock()
		for ctx.Err() == nil && (p.inflight > 0 || p.pendingCount() > 0) {
			p.cond.Wait()
		}
		p.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
		<-done
		return ctx.Err()
	}
}

func (p *InProcess) pendingCount() int {
	n := 0
	for _, l := range p.pending {
		n += len(l)
	}
	return n
}

// Discarded returns jobs that exhausted their attempts.
func (p *InProcess) Discarded() []Discarded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Discarded(nil), p.discarded...)
}

// Completed returns the number of successfully finished jobs.
func (p *InProcess) Completed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completed
}
