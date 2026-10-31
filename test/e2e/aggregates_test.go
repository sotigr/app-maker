package e2e

import (
	"fmt"
	"sync"
	"testing"
)

// Milestone 9: aggregates stay correct under concurrent child changes.
func TestAggregatesUnderConcurrentChanges(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		// setup created one task; add 20 more concurrently.
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				mustOK(t, w.admin.create("task", w.project, map[string]any{"title": fmt.Sprint("t", i)}), 201)
			}(i)
		}
		wg.Wait()
		count := func() (float64, float64) {
			d := mustOK(t, w.admin.do("GET", "/v1/features/project/"+w.project, nil), 200).Body["data"].(map[string]any)
			tc, _ := d["taskCount"].(float64)
			dc, _ := d["doneCount"].(float64)
			return tc, dc
		}
		eventually(t, "taskCount = 21", func() bool { tc, _ := count(); return tc == 21 })
		// Completing tasks changes the filtered aggregate.
		task := w.newTask(t, map[string]any{"title": "to finish"})
		w.assign(t, task, w.editor)
		mustOK(t, action(w.editor, task, "start", nil), 200)
		mustOK(t, action(w.editor, task, "complete", nil), 200)
		eventually(t, "doneCount = 1 and taskCount = 22", func() bool { tc, dc := count(); return tc == 22 && dc == 1 })
		mustOK(t, w.admin.do("DELETE", "/v1/features/task/"+task, nil, "If-Match", w.taskVersion(t, task)), 204)
		eventually(t, "counts after delete", func() bool { tc, dc := count(); return tc == 21 && dc == 0 })
	})
}
