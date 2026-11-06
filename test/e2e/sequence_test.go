package e2e

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
)

// ADR 0021: x-sequence numbers are gapless and unique under concurrent
// creates, and "on": "action:<name>" numbers a feature when that action
// runs, once.
func TestSequences(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		s := startFull(t, driver, nil, func(m *manifestdomain.Manifest) {
			for i, f := range m.Types {
				if strings.HasSuffix(f.Path, "/task.json") {
					m.Types[i].Data = []byte(strings.Replace(string(f.Data), `"properties": {`,
						`"properties": { "startNo": { "type": "string", "x-indexed": true, "x-sequence": { "format": "{series}-{n:03}", "series": "'S' + now().Format('2006')", "on": "action:start" } },`, 1))
				}
			}
		}, nil)
		admin := s.admin()
		p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
		const n = 20
		var mu sync.Mutex
		got := map[string]string{}
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := admin.create("task", p, map[string]any{"title": fmt.Sprint("t", i)})
				if r.Status != 201 {
					t.Errorf("create: %s", r.Raw)
					return
				}
				mu.Lock()
				got[r.str("data", "number")] = r.str("id")
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		for i := 1; i <= n; i++ {
			if _, ok := got[fmt.Sprintf("T-%04d", i)]; !ok {
				t.Fatalf("missing T-%04d in %v", i, got)
			}
		}
		task := got["T-0001"]
		if r := mustOK(t, admin.do("GET", "/v1/features/task/"+task, nil), 200); r.str("data", "startNo") != "" {
			t.Fatalf("not numbered before the action: %s", r.Raw)
		}
		started := mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/start", map[string]any{}), 200)
		if !strings.Contains(started.Raw, `"startNo":"S2026-001"`) {
			t.Fatalf("start numbers the task: %s", started.Raw)
		}
		other := got["T-0002"]
		mustOK(t, admin.do("POST", "/v1/actions/task/"+other+"/start", map[string]any{}), 200)
		if r := mustOK(t, admin.do("GET", "/v1/features/task/"+other, nil), 200); r.str("data", "startNo") != "S2026-002" {
			t.Fatalf("second start: %s", r.Raw)
		}
	})
}
