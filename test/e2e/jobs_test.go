package e2e

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	changeapi "organon/internal/change/api"
)

// Spec 2.5, 9.2: every commit enqueues publish_events in the same
// transaction and a worker runs it (River on SQLite, in-process on memory).
func TestCommittedWritesRunTheirJobs(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		var events atomic.Int64
		w.s.rt.JobKinds.Register(changeapi.JobPublishEvents, func(_ context.Context, raw json.RawMessage) error {
			var a changeapi.PublishEventsArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			for _, e := range a.Events {
				if e.Kind == changeapi.EventCreated && e.After["title"] == "watched" {
					events.Add(1)
				}
			}
			return nil
		}, 0)
		mustOK(t, w.admin.create("task", w.project, map[string]any{"title": "watched"}), 201)
		deadline := time.Now().Add(10 * time.Second)
		for events.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if events.Load() != 1 {
			t.Fatalf("publish_events ran %d times for the write", events.Load())
		}
	})
}
