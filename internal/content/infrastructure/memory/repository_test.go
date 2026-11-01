package memory_test

import (
	"context"
	"sync"
	"testing"

	"organon/internal/content/infrastructure/memory"
	"organon/internal/content/repotest"
	"organon/internal/kernel"
)

type recorder struct {
	mu   sync.Mutex
	jobs []kernel.JobRequest
}

func (r *recorder) Enqueue(_ context.Context, jobs []kernel.JobRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, jobs...)
	return nil
}

func (r *recorder) all() []kernel.JobRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]kernel.JobRequest(nil), r.jobs...)
}

// Milestone 2: the memory repository passes the conformance suite.
func TestConformance(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Harness {
		rec := &recorder{}
		return repotest.Harness{Repo: memory.New(rec), Jobs: rec.all}
	})
}
