package e2e

import (
	"strings"
	"testing"
)

// Spec 14.3: one audit row per mutation, written with the commit, plus
// authentication events; admins only; no field values.
func TestAuditLog(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		mustOK(t, w.editor.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "secret title"}, "If-Match", w.taskVersion(t, w.task)), 200)
		if r := w.anon.do("POST", "/v1/auth/password", map[string]any{"login": "eddie@example.com", "password": "wrong-password-1", "cli": true}); r.Status != 401 {
			t.Fatalf("bad login: %s", r.Raw)
		}

		if r := w.editor.do("GET", "/v1/audit", nil); r.Status != 403 {
			t.Fatalf("non-admin audit: %s", r.Raw)
		}
		r := mustOK(t, w.admin.do("GET", "/v1/audit?target="+w.task, nil), 200)
		items := r.Body["items"].([]any)
		if len(items) != 2 { // created, updated
			t.Fatalf("task audit: %s", r.Raw)
		}
		upd := items[0].(map[string]any)
		if upd["action"] != "updated" || upd["actor"] != w.editor.id || upd["type"] != "urn:app:feature:task" {
			t.Fatalf("update row: %v", upd)
		}
		if ch, _ := upd["changed"].([]any); len(ch) == 0 || !strings.Contains(r.Raw, "/title") {
			t.Fatalf("changed paths: %v", upd)
		}
		if strings.Contains(r.Raw, "secret title") {
			t.Fatal("field values leaked into the audit log")
		}
		logins := mustOK(t, w.admin.do("GET", "/v1/audit?action=login_failed", nil), 200)
		if f := logins.Body["items"].([]any); len(f) != 1 || f[0].(map[string]any)["actor"] != w.editor.id {
			t.Fatalf("failed login: %s", logins.Raw)
		}
		page := mustOK(t, w.admin.do("GET", "/v1/audit?limit=2", nil), 200)
		next, _ := page.Body["next"].(string)
		more := mustOK(t, w.admin.do("GET", "/v1/audit?limit=2&before="+next, nil), 200)
		if next == "" || len(more.Body["items"].([]any)) != 2 || strings.Contains(more.Raw, next+`","at"`) {
			t.Fatalf("paging: %s / %s", page.Raw, more.Raw)
		}
	})
}
