package kernel

import (
	"context"
	"encoding/json"
	"time"
)

// Job queues (spec 2.5).
const (
	QueueEvents      = "events"
	QueueFlows       = "flows"
	QueueEffects     = "effects"
	QueueMaintenance = "maintenance"
)

// JobRequest describes a unit of background work, inserted in the same
// transaction as the write that causes it (spec 2.5).
type JobRequest struct {
	Kind        string // e.g. "run_flow"
	Args        json.RawMessage
	Queue       string // "events", "flows", "effects", "maintenance"
	UniqueKey   string // optional; deduplicates pending jobs with the same key
	ScheduledAt *time.Time
	MaxAttempts int
	// Trace carries the trace context of the request that caused the job
	// (spec 14.3), so a trace follows a write through its jobs.
	Trace map[string]string
}

// JobEnqueuer enqueues jobs outside a repository commit (for example from
// the webhook endpoint). Jobs caused by writes travel in the write batch.
type JobEnqueuer interface {
	Enqueue(ctx context.Context, jobs []JobRequest) error
}

type attemptKey struct{}

// JobAttempt is the attempt number of the running job and its maximum.
type JobAttempt struct{ Attempt, Max int }

// WithJobAttempt is set by job adapters before calling a handler.
func WithJobAttempt(ctx context.Context, a JobAttempt) context.Context {
	return context.WithValue(ctx, attemptKey{}, a)
}

// JobAttemptFrom returns the running job's attempt; ok is false outside jobs.
func JobAttemptFrom(ctx context.Context) (JobAttempt, bool) {
	a, ok := ctx.Value(attemptKey{}).(JobAttempt)
	return a, ok
}

// FinalAttempt reports whether a failure now is the job's last.
func FinalAttempt(ctx context.Context) bool {
	a, ok := JobAttemptFrom(ctx)
	return ok && a.Max > 0 && a.Attempt >= a.Max
}
