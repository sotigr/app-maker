package e2e

import (
	"strings"
	"testing"
)

const failingFlow = `{
  "flow": "alwaysFails",
  "trigger": { "event": ["created"], "on": "urn:app:feature:comment" },
  "nodes": { "boom": { "node": "fail", "in": { "message": "'no luck'" } } }
}`

// Spec 10.10: a background run that fails its last attempt is recorded as
// dead_letter and can be retried by an admin. In-process jobs only: River
// schedules retries every few seconds (its scheduler interval is fixed),
// so five attempts outlast the test; platform/jobs covers River attempts.
func TestDeadLetterAndRetry(t *testing.T) {
	func(t *testing.T, driver string) {
		s := startWith(t, driver, map[string]string{"fails.json": failingFlow})
		admin := s.admin()
		p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
		task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")
		mustOK(t, admin.create("comment", task, map[string]any{"body": "hi"}), 201)
		runs := func(status string) []any {
			r := admin.do("POST", "/v1/query", map[string]any{"type": "flowRun", "where": map[string]any{"and": []any{
				map[string]any{"field": "/flow", "op": "eq", "value": "alwaysFails"},
				map[string]any{"field": "/status", "op": "eq", "value": status}}}})
			items, _ := r.Body["items"].([]any)
			return items
		}
		eventually(t, "dead letter", func() bool { return len(runs("dead_letter")) == 1 })
		if n := len(runs("failed")); n < 1 {
			t.Fatalf("earlier attempts recorded as failed: %d", n)
		}
		dead := runs("dead_letter")[0].(map[string]any)["id"].(string)
		viewer := s.user(admin, "vic")
		if r := viewer.do("POST", "/v1/flows/runs/"+dead+"/retry", nil); r.Status != 403 {
			t.Fatalf("non-admin retry: %s", r.Raw)
		}
		mustOK(t, admin.do("POST", "/v1/flows/runs/"+dead+"/retry", nil), 202)
		eventually(t, "retried run dead-lettered again", func() bool { return len(runs("dead_letter")) == 2 })
		if r := admin.do("POST", "/v1/flows/runs/"+dead+"/retry", nil); r.Status != 202 {
			t.Fatalf("second retry: %s", r.Raw)
		}
		ok := runs("failed")[0].(map[string]any)["id"].(string)
		if r := admin.do("POST", "/v1/flows/runs/"+ok+"/retry", nil); r.Status != 409 || !strings.Contains(r.Raw, "dead-lettered") {
			t.Fatalf("retry of a failed attempt: %s", r.Raw)
		}
	}(t, "memory")
}
