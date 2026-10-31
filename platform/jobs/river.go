package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"app-maker/internal/kernel"
)

// RiverKind is the single River job kind used by the framework. The real
// kind travels in the arguments so workers can be registered at run time
// through Registry (River binds kinds to Go types at compile time).
const RiverKind = "fw"

// riverArgs are the stored arguments of every framework job.
type riverArgs struct {
	JobKind   string          `json:"kind" river:"unique"`
	UniqueKey string          `json:"key,omitempty" river:"unique"`
	Args      json.RawMessage `json:"args"`
}

func (riverArgs) Kind() string { return RiverKind }

type riverWorker struct {
	river.WorkerDefaults[riverArgs]
	reg            *Registry
	defaultTimeout time.Duration
}

func (w *riverWorker) Work(ctx context.Context, job *river.Job[riverArgs]) error {
	h, _, ok := w.reg.Lookup(job.Args.JobKind)
	if !ok {
		return river.JobCancel(fmt.Errorf("%w: %s", ErrNoHandler, job.Args.JobKind))
	}
	err := h(ctx, job.Args.Args)
	if IsPermanent(err) {
		return river.JobCancel(err)
	}
	return err
}

// Timeout uses the kind's registered timeout (spec 10.11: run_flow jobs
// get their flow's budget, not River's default).
func (w *riverWorker) Timeout(job *river.Job[riverArgs]) time.Duration {
	if _, t, ok := w.reg.Lookup(job.Args.JobKind); ok && t > 0 {
		return t
	}
	return w.defaultTimeout
}

// RiverConfig configures the River adapter.
type RiverConfig struct {
	Concurrency    map[string]int
	DefaultTimeout time.Duration
	// InsertOnly creates a client that inserts but never works jobs (CLI,
	// conformance tests).
	InsertOnly bool
}

// RiverSQLite runs jobs with River on SQLite (spec 2.5). It implements
// kernel.JobEnqueuer and transactional insertion for repositories.
type RiverSQLite struct {
	client *river.Client[*sql.Tx]
	db     *sql.DB
}

var _ kernel.JobEnqueuer = (*RiverSQLite)(nil)

// NewRiverSQLite migrates River's tables and builds a client.
func NewRiverSQLite(ctx context.Context, db *sql.DB, reg *Registry, cfg RiverConfig, log *slog.Logger) (*RiverSQLite, error) {
	drv := riversqlite.New(db)
	m, err := rivermigrate.New(drv, nil)
	if err != nil {
		return nil, err
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("river migrations: %w", err)
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 60 * time.Second
	}
	rc := &river.Config{Logger: log, FetchPollInterval: 200 * time.Millisecond, JobTimeout: cfg.DefaultTimeout,
		MaxAttempts: DefaultMaxAttempts}
	if !cfg.InsertOnly {
		workers := river.NewWorkers()
		river.AddWorker(workers, &riverWorker{reg: reg, defaultTimeout: cfg.DefaultTimeout})
		queues := map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}}
		for _, q := range []string{kernel.QueueEvents, kernel.QueueFlows, kernel.QueueEffects, kernel.QueueMaintenance} {
			n := cfg.Concurrency[q]
			if n <= 0 {
				n = 2
			}
			queues[q] = river.QueueConfig{MaxWorkers: n}
		}
		rc.Workers = workers
		rc.Queues = queues
	}
	client, err := river.NewClient(drv, rc)
	if err != nil {
		return nil, err
	}
	return &RiverSQLite{client: client, db: db}, nil
}

// Start runs the workers until Stop.
func (r *RiverSQLite) Start(ctx context.Context) error { return r.client.Start(ctx) }

// Stop finishes running jobs and stops.
func (r *RiverSQLite) Stop(ctx context.Context) error { return r.client.Stop(ctx) }

func toParams(jobs []kernel.JobRequest) []river.InsertManyParams {
	seen := map[string]bool{}
	out := make([]river.InsertManyParams, 0, len(jobs))
	for _, j := range jobs {
		if j.UniqueKey != "" {
			k := j.Kind + "\x00" + j.UniqueKey
			if seen[k] {
				continue // River rejects duplicate unique keys within a batch
			}
			seen[k] = true
		}
		args := j.Args
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		opts := &river.InsertOpts{Queue: j.Queue, MaxAttempts: j.MaxAttempts}
		if opts.Queue == "" {
			opts.Queue = kernel.QueueMaintenance
		}
		if j.ScheduledAt != nil {
			opts.ScheduledAt = *j.ScheduledAt
		}
		if j.UniqueKey != "" {
			opts.UniqueOpts = river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}}
		}
		out = append(out, river.InsertManyParams{Args: riverArgs{JobKind: j.Kind, UniqueKey: j.UniqueKey, Args: args}, InsertOpts: opts})
	}
	return out
}

// InsertTx inserts jobs in the caller's transaction (spec 5.2).
func (r *RiverSQLite) InsertTx(ctx context.Context, tx *sql.Tx, jobs []kernel.JobRequest) error {
	if len(jobs) == 0 {
		return nil
	}
	_, err := r.client.InsertManyTx(ctx, tx, toParams(jobs))
	return err
}

// Enqueue inserts jobs outside a write (webhooks, schedules).
func (r *RiverSQLite) Enqueue(ctx context.Context, jobs []kernel.JobRequest) error {
	if len(jobs) == 0 {
		return nil
	}
	_, err := r.client.InsertMany(ctx, toParams(jobs))
	return err
}

// Committed lists every stored job (any state), oldest first; used by
// conformance tests and the admin console.
func (r *RiverSQLite) Committed(ctx context.Context) ([]kernel.JobRequest, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT json(args), queue FROM river_job ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []kernel.JobRequest
	for rows.Next() {
		var raw []byte
		var q string
		if err := rows.Scan(&raw, &q); err != nil {
			return nil, err
		}
		var a riverArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		out = append(out, kernel.JobRequest{Kind: a.JobKind, UniqueKey: a.UniqueKey, Args: a.Args, Queue: q})
	}
	return out, rows.Err()
}

type scheduleFunc func(time.Time) time.Time

func (f scheduleFunc) Next(t time.Time) time.Time { return f(t) }

// AddPeriodic registers a periodic job; River enqueues each occurrence once
// through its own leadership in the database (spec 2.5).
func (r *RiverSQLite) AddPeriodic(name string, next func(time.Time) time.Time, job kernel.JobRequest) error {
	params := toParams([]kernel.JobRequest{job})
	if len(params) == 0 {
		return nil
	}
	p := params[0]
	defer func() { _ = recover() }() // insert-only clients cannot schedule
	r.client.PeriodicJobs().Add(river.NewPeriodicJob(scheduleFunc(next),
		func() (river.JobArgs, *river.InsertOpts) { return p.Args, p.InsertOpts },
		&river.PeriodicJobOpts{ID: name}))
	return nil
}
