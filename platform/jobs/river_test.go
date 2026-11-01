package jobs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"organon/internal/kernel"
	"organon/platform/sqlitedb"
)

// Handlers see their attempt (dead_letter detection, spec 10.10) and the
// enqueuing trace carrier survives the River round trip.
func TestRiverAttemptAndTrace(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := NewRegistry()
	got := make(chan kernel.JobAttempt, 1)
	reg.Register("probe", func(ctx context.Context, _ json.RawMessage) error {
		a, _ := kernel.JobAttemptFrom(ctx)
		got <- a
		return nil
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := NewRiverSQLite(ctx, db.DB, reg, RiverConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop(context.Background()) }()
	if err := r.Enqueue(ctx, []kernel.JobRequest{{Kind: "probe", Queue: kernel.QueueMaintenance, MaxAttempts: 7,
		Trace: map[string]string{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-got:
		if a.Attempt != 1 || a.Max != 7 {
			t.Fatalf("attempt %+v", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job did not run")
	}
	all, err := r.Committed(ctx)
	if err != nil || len(all) != 1 || all[0].Trace["traceparent"] == "" {
		t.Fatalf("stored jobs: %+v %v", all, err)
	}
}
