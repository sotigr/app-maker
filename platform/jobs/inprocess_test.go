package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"app-maker/internal/kernel"
)

func TestInProcessRunsRetriesAndDiscards(t *testing.T) {
	reg := NewRegistry()
	var ok, flaky, bad atomic.Int32
	reg.Register("ok", func(context.Context, json.RawMessage) error { ok.Add(1); return nil }, 0)
	reg.Register("flaky", func(context.Context, json.RawMessage) error {
		if flaky.Add(1) < 3 {
			return errors.New("try again")
		}
		return nil
	}, 0)
	reg.Register("bad", func(context.Context, json.RawMessage) error { bad.Add(1); return errors.New("no") }, 0)
	p := NewInProcess(reg, InProcessConfig{BaseBackoff: time.Millisecond, Concurrency: map[string]int{"events": 4}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	err := p.Enqueue(ctx, []kernel.JobRequest{
		{Kind: "ok", Queue: "events"}, {Kind: "ok", Queue: "events"},
		{Kind: "flaky", Queue: "flows"},
		{Kind: "bad", Queue: "effects", MaxAttempts: 2},
		{Kind: "missing", Queue: "maintenance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
	defer dcancel()
	if err := p.Drain(dctx); err != nil {
		t.Fatal(err)
	}
	if ok.Load() != 2 || flaky.Load() != 3 || bad.Load() != 2 {
		t.Fatalf("ok=%d flaky=%d bad=%d", ok.Load(), flaky.Load(), bad.Load())
	}
	if d := p.Discarded(); len(d) != 2 {
		t.Fatalf("discarded: %+v", d)
	}
}

// Spec 2.5: jobs with the same unique key coalesce while pending.
func TestInProcessUniqueKeys(t *testing.T) {
	reg := NewRegistry()
	var n atomic.Int32
	reg.Register("idx", func(context.Context, json.RawMessage) error { n.Add(1); return nil }, 0)
	p := NewInProcess(reg, InProcessConfig{}, nil)
	ctx := context.Background()
	_ = p.Enqueue(ctx, []kernel.JobRequest{{Kind: "idx", UniqueKey: "f1"}, {Kind: "idx", UniqueKey: "f1"}, {Kind: "idx", UniqueKey: "f2"}})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.Start(rctx)
	dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
	defer dcancel()
	if err := p.Drain(dctx); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("ran %d, want 2", n.Load())
	}
}
