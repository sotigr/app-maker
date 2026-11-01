package e2e

import (
	"fmt"

	accessapi "organon/internal/access/api"
	"organon/internal/content/application"

	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"organon/internal/kernel"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (w *world) taskVersion(t *testing.T, id string) string {
	t.Helper()
	return mustOK(t, w.admin.do("GET", "/v1/features/task/"+id, nil), 200).Header.Get("ETag")
}

func (w *world) newTask(t *testing.T, data map[string]any) string {
	t.Helper()
	return mustOK(t, w.admin.create("task", w.project, data), 201).str("id")
}

func (w *world) assign(t *testing.T, task string, to *client) {
	t.Helper()
	mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+task, map[string]any{"assignee": to.id}, "If-Match", w.taskVersion(t, task)), 200)
}

func action(c *client, task, name string, input map[string]any) resp {
	return c.do("POST", "/v1/actions/task/"+task+"/"+name, input)
}

// Spec 10.3, 4.10: actions are flows or declared transitions, authorized
// by action policies and guarded by state-machine guards.
func TestActionsAndTransitions(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		task := w.newTask(t, map[string]any{"title": "Ship it"})
		w.assign(t, task, w.editor)

		// complete is not allowed from todo.
		if r := action(w.editor, task, "complete", nil); r.Status != 409 || r.code() != kernel.CodeInvalidTransition {
			t.Fatalf("complete from todo: %s", r.Raw)
		}
		// start is a declared transition without a flow; policy: editor.
		if r := action(w.viewer, task, "start", nil); r.Status != 403 {
			t.Fatalf("viewer start: %s", r.Raw)
		}
		r := mustOK(t, action(w.editor, task, "start", nil), 200)
		if r.str("feature", "data", "status") != "in_progress" {
			t.Fatalf("start: %s", r.Raw)
		}
		// complete is the completeTask flow; policy: the assignee only.
		if r := action(w.viewer, task, "complete", nil); r.Status != 403 {
			t.Fatalf("non-assignee complete: %s", r.Raw)
		}
		r = mustOK(t, action(w.editor, task, "complete", map[string]any{"note": "Shipped"}), 200)
		if out, _ := r.Body["output"].(map[string]any); out["late"] != false {
			t.Fatalf("complete output: %s", r.Raw)
		}
		got := mustOK(t, w.admin.do("GET", "/v1/features/task/"+task, nil), 200)
		if got.str("data", "status") != "done" {
			t.Fatalf("status after complete: %s", got.Raw)
		}
		kids := mustOK(t, w.admin.do("GET", "/v1/features/"+task+"/children?type=comment", nil), 200)
		if items, _ := kids.Body["items"].([]any); len(items) != 1 || !strings.Contains(kids.Raw, "Shipped") {
			t.Fatalf("comment staged by the flow: %s", kids.Raw)
		}
		// The flow's writes ran as the assignee: the comment is owned by them.
		if !strings.Contains(kids.Raw, w.editor.id) {
			t.Fatalf("comment owner: %s", kids.Raw)
		}
		// reopen: admins only (guard 'admin' in principal.roles).
		if r := action(w.editor, task, "reopen", nil); r.Status != 403 {
			t.Fatalf("editor reopen: %s", r.Raw)
		}
		mustOK(t, action(w.admin, task, "reopen", nil), 200)
		// The generated transition action dispatches by name.
		r = mustOK(t, action(w.editor, task, "transition", map[string]any{"action": "complete"}), 200)
		_ = r
		if s := mustOK(t, w.admin.do("GET", "/v1/features/task/"+task, nil), 200).str("data", "status"); s != "done" {
			t.Fatalf("transition: status %s", s)
		}
		// transfer: built in for Ownable types; the owner (admin here) may.
		r = mustOK(t, action(w.admin, task, "transfer", map[string]any{"to": w.editor.id}), 200)
		if r.str("feature", "data", "owner") != w.editor.id {
			t.Fatalf("transfer: %s", r.Raw)
		}
		if r := action(w.viewer, task, "transfer", map[string]any{"to": w.viewer.id}); r.Status != 403 {
			t.Fatalf("non-owner transfer: %s", r.Raw)
		}
		if r := action(w.editor, task, "nope", nil); r.Status != 403 && r.Status != 404 {
			t.Fatalf("unknown action: %s", r.Raw)
		}
	})
}

// A late completion sends an email after commit (Effect node, spec 14.5).
func TestEffectsDeliveredAfterCommit(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		task := w.newTask(t, map[string]any{"title": "Late one", "dueAt": "2026-01-01T00:00:00Z"})
		w.assign(t, task, w.editor)
		mustOK(t, action(w.editor, task, "start", nil), 200)
		r := mustOK(t, action(w.editor, task, "complete", nil), 200)
		if out, _ := r.Body["output"].(map[string]any); out["late"] != true {
			t.Fatalf("late: %s", r.Raw)
		}
		eventually(t, "late-task email", func() bool {
			for _, m := range w.s.emails.Sent() {
				if strings.Contains(m.Subject, "Late task completed: Late one") && strings.Contains(m.Body, "in P") {
					return true
				}
			}
			return false
		})
	})
}

// Spec 10.2: event triggers run after commit, as the principal that
// caused the event.
func TestEventTriggerRunsInWorker(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		task := w.newTask(t, map[string]any{"title": "Assigned work"})
		w.assign(t, task, w.viewer)
		eventually(t, "assignment email", func() bool {
			for _, m := range w.s.emails.Sent() {
				if m.To == "vera@example.com" && strings.Contains(m.Subject, "Assigned work") {
					return true
				}
			}
			return false
		})
		// The run is recorded as a flowRun feature (spec 10.12).
		eventually(t, "flow run record", func() bool {
			r := w.admin.do("POST", "/v1/query", map[string]any{"type": "flowRun",
				"where": map[string]any{"field": "/flow", "op": "eq", "value": "notifyAssignee"}})
			return strings.Contains(r.Raw, `"status":"succeeded"`)
		})
	})
}

// Spec 10.5: a reject node aborts the write with its declared error.
func TestBeforeWriteReject(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		r := w.viewer.create("comment", w.task, map[string]any{"body": "BUY NOW, cheap watches"})
		if r.Status != 422 || r.code() != "spam" {
			t.Fatalf("spam comment: %s", r.Raw)
		}
		mustOK(t, w.viewer.create("comment", w.task, map[string]any{"body": "looks good"}), 201)
	})
}

// Spec 10.2: inbound webhooks verify an HMAC and run in a worker.
func TestWebhookTrigger(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		body := `{"project":"` + w.project + `","title":"From GitHub"}`
		sign := func(secret string) string {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write([]byte(body))
			return "sha256=" + hex.EncodeToString(m.Sum(nil))
		}
		post := func(sig string) int {
			req, _ := http.NewRequest("POST", w.s.url+"/v1/hooks/issues", strings.NewReader(body))
			req.Header.Set("X-Organon-Signature", sig)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			return res.StatusCode
		}
		if st := post(sign("wrong")); st != 401 {
			t.Fatalf("bad signature: %d", st)
		}
		if st := post(sign("s3cret")); st != 202 {
			t.Fatalf("good signature: %d", st)
		}
		eventually(t, "task from webhook", func() bool {
			r := w.admin.do("GET", "/v1/features/"+w.project+"/children?type=task", nil)
			return strings.Contains(r.Raw, "[inbound] From GitHub")
		})
	})
}

// Milestone 8: a flow loop is stopped and recorded as loop_stopped (spec 10.9).
func TestFlowLoopStopped(t *testing.T) {
	loop := `{
	  "flow": "selfLoop",
	  "trigger": { "event": ["updated"], "on": "urn:app:feature:task", "when": "changed('/title')" },
	  "nodes": { "bump": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/title": "feature.title + '!'" } } } }
	}`
	eachDriver(t, func(t *testing.T, driver string) {
		s := startWith(t, driver, map[string]string{"self-loop.json": loop})
		w := &world{s: s, admin: s.admin()}
		w.project = mustOK(t, w.admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
		task := w.newTask(t, map[string]any{"title": "x"})
		mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+task, map[string]any{"title": "y"}, "If-Match", `"1"`), 200)
		eventually(t, "loop_stopped run", func() bool {
			r := w.admin.do("POST", "/v1/query", map[string]any{"type": "flowRun",
				"where": map[string]any{"field": "/status", "op": "eq", "value": "loop_stopped"}})
			return strings.Contains(r.Raw, "selfLoop")
		})
		// The loop ran once, then stopped: the title gained exactly one "!".
		got := mustOK(t, w.admin.do("GET", "/v1/features/task/"+task, nil), 200)
		if got.str("data", "title") != "y!" {
			t.Fatalf("title after loop stop: %s", got.str("data", "title"))
		}
	})
}

func (w *world) taskVersionInt(t *testing.T, id string) kernel.Version {
	t.Helper()
	r := mustOK(t, w.admin.do("GET", "/v1/features/task/"+id, nil), 200)
	return kernel.Version(r.Body["version"].(float64))
}

func etagOf(v float64) string { return `"` + strings.TrimSuffix(fmt.Sprint(v), ".0") + `"` }

func systemFixture() application.WriteContext {
	return application.WriteContext{Principal: accessapi.System(), Fixture: true}
}

func updateOp(id string, v kernel.Version, data map[string]any) []application.Op {
	return []application.Op{{Op: application.OpUpdate, Type: "task", ID: id, Version: v, Data: data}}
}
