package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// Gaps 5 and 23 (ADR 0023): moving a feature recomputes aggregates on the
// old parent too, and aggregate recomputes keep the parent's version, so
// a user write with the version read before them still commits.
func TestGap05And23Aggregates(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		count := func(p string) string {
			r := mustOK(t, w.admin.do("GET", "/v1/features/project/"+p, nil), 200)
			return fmt.Sprint(r.Body["data"].(map[string]any)["taskCount"])
		}
		etag := mustOK(t, w.admin.do("GET", "/v1/features/project/"+w.project, nil), 200).Header.Get("ETag")
		task := w.newTask(t, map[string]any{"title": "moving"})
		eventually(t, "taskCount 2", func() bool { return count(w.project) == "2" })
		// The recompute did not change the version the user read.
		mustOK(t, w.admin.do("PATCH", "/v1/features/project/"+w.project, map[string]any{"description": "still mine"}, "If-Match", etag), 200)
		if c := count(w.project); c != "2" {
			t.Fatalf("a user update undid the aggregate: %s", c)
		}
		other := mustOK(t, w.admin.create("project", "", map[string]any{"title": "Other"}), 201).str("id")
		mv := w.admin.do("POST", "/v1/features/"+task+"/move", map[string]any{"parent": other, "version": 1})
		if mv.Status != 200 && !strings.Contains(mv.Raw, `"version"`) {
			t.Fatalf("move: %s", mv.Raw)
		}
		eventually(t, "counts after the move", func() bool { return count(w.project) == "1" && count(other) == "1" })
	})
}

// Gap 14 (ADR 0023): integer action input sent over HTTP compares as an
// integer in flow expressions (it used to stay json.Number and fail).
func TestGap14IntegerInputOverHTTP(t *testing.T) {
	flow := `{"flow":"mileage","trigger":{"action":"mileage","on":"urn:app:feature:task"},
	  "input":{"type":"object","properties":{"km":{"type":"integer"}},"required":["km"]},
	  "output":{"type":"object","properties":{"far":{"type":"boolean"}}},
	  "nodes":{"far":{"node":"expr","in":{"expr":"input.km > 1000 && input.km + 1 == 2001"}}},
	  "result":{"far":"nodes.far.out"}}`
	s := startWith(t, "memory", map[string]string{"mileage.json": flow})
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")
	r := mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/mileage", map[string]any{"km": 2000}), 200)
	if !strings.Contains(r.Raw, `"far":true`) {
		t.Fatalf("integer input: %s", r.Raw)
	}
}
