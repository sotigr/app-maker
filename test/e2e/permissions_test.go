package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/content/application"
	"app-maker/internal/kernel"
)

type world struct {
	s                                      *server
	admin, alice, editor, viewer, stranger *client
	anon                                   *client
	project, task, comment, aliceProj      string
}

// setup: admin owns project P with task T and comment C; editor and viewer
// hold grants on P; alice (app role editor) owns her own project; the
// stranger has no grants.
func setup(t *testing.T, driver string) *world {
	s := start(t, driver)
	w := &world{s: s, admin: s.admin()}
	w.alice = s.user(w.admin, "alice", "editor")
	w.editor = s.user(w.admin, "eddie")
	w.viewer = s.user(w.admin, "vera")
	w.stranger = s.user(w.admin, "sam")
	w.anon = s.anon()
	w.project = mustOK(t, w.admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	w.task = mustOK(t, w.admin.create("task", w.project, map[string]any{"title": "T"}), 201).str("id")
	w.comment = mustOK(t, w.admin.create("comment", w.task, map[string]any{"body": "hello"}), 201).str("id")
	mustOK(t, w.admin.create("grant", w.project, map[string]any{"subject": w.editor.id, "role": "editor"}), 201)
	mustOK(t, w.admin.create("grant", w.project, map[string]any{"subject": w.viewer.id, "role": "viewer"}), 201)
	w.aliceProj = mustOK(t, w.alice.create("project", "", map[string]any{"title": "Alice's"}), 201).str("id")
	return w
}

// Milestone 3: the tracker's permission matrix passes through HTTP.
func TestPermissionMatrix(t *testing.T) { eachDriver(t, testPermissionMatrix) }

func testPermissionMatrix(t *testing.T, driver string) {
	w := setup(t, driver)
	type row struct {
		who    string
		c      *client
		status int
	}
	check := func(name string, rows []row, call func(c *client) resp) {
		t.Helper()
		for _, r := range rows {
			got := call(r.c)
			if got.Status != r.status {
				t.Errorf("%s as %s: got %d want %d (%s)", name, r.who, got.Status, r.status, got.Raw)
			}
		}
	}
	all := func(admin, alice, editor, viewer, stranger, anon int) []row {
		return []row{{"admin", w.admin, admin}, {"alice", w.alice, alice}, {"editor", w.editor, editor},
			{"viewer", w.viewer, viewer}, {"stranger", w.stranger, stranger}, {"anonymous", w.anon, anon}}
	}

	// Reads (project: viewer or owner; task, comment: viewer).
	check("read project", all(200, 200, 200, 200, 404, 401), func(c *client) resp { return c.do("GET", "/v1/features/project/"+w.project, nil) })
	check("read task", all(200, 200, 200, 200, 404, 401), func(c *client) resp { return c.do("GET", "/v1/features/task/"+w.task, nil) })
	check("read comment", all(200, 200, 200, 200, 404, 401), func(c *client) resp { return c.do("GET", "/v1/features/comment/"+w.comment, nil) })
	check("read alice's project", all(200, 200, 404, 404, 404, 401), func(c *client) resp { return c.do("GET", "/v1/features/project/"+w.aliceProj, nil) })

	// Creates (project: editor on root = app role; task: editor on parent;
	// comment: viewer on parent).
	check("create project", all(201, 201, 403, 403, 403, 401), func(c *client) resp {
		return c.create("project", "", map[string]any{"title": "new"})
	})
	check("create task", all(201, 201, 201, 403, 404, 401), func(c *client) resp {
		return c.create("task", w.project, map[string]any{"title": "new"})
	})
	check("create comment", all(201, 201, 201, 201, 404, 401), func(c *client) resp {
		return c.create("comment", w.task, map[string]any{"body": "hi"})
	})

	// Updates (project: editor or owner).
	version := func() string {
		r := w.admin.do("GET", "/v1/features/project/"+w.project, nil)
		return r.Header.Get("ETag")
	}
	check("update project", all(200, 200, 200, 403, 404, 401), func(c *client) resp {
		return c.do("PATCH", "/v1/features/project/"+w.project, map[string]any{"description": "x"}, "If-Match", version())
	})

	// Deletes (task: admin or owner; alice is an app editor, not admin).
	// Each row gets its own task.
	check("delete task", all(204, 403, 403, 403, 404, 401), func(c *client) resp {
		r := mustOK(t, w.admin.create("task", w.project, map[string]any{"title": "victim"}), 201)
		return c.do("DELETE", "/v1/features/task/"+r.str("id"), nil, "If-Match", `"1"`)
	})
	// The creator of a task owns it and may delete it.
	own := mustOK(t, w.editor.create("task", w.project, map[string]any{"title": "mine"}), 201)
	mustOK(t, w.editor.do("DELETE", "/v1/features/task/"+own.str("id"), nil, "If-Match", `"1"`), 204)

	// Queries are scoped by the read policy (spec 7.3 step 4).
	count := func(c *client, typ string) int {
		r := mustOK(t, c.do("POST", "/v1/query", map[string]any{"type": typ}), 200)
		items, _ := r.Body["items"].([]any)
		return len(items)
	}
	if n := count(w.stranger, "task"); n != 0 {
		t.Errorf("stranger sees %d tasks", n)
	}
	if n := count(w.viewer, "project"); n != 1 {
		t.Errorf("viewer sees %d projects, want 1", n)
	}
	if a, v := count(w.admin, "project"), count(w.alice, "project"); a < 2 || v < 2 {
		t.Errorf("admin sees %d, alice (app editor, includes viewer) sees %d projects", a, v)
	}
}

// Milestone 3: no response can expose a private field.
func TestNoPrivateFieldLeaks(t *testing.T) { eachDriver(t, testNoPrivateFieldLeaks) }

func testNoPrivateFieldLeaks(t *testing.T, driver string) {
	w := setup(t, driver)
	const secret = 0.987654321
	ctx := context.Background()
	res, err := w.s.rt.Content.Execute(ctx, application.WriteContext{Principal: accessapi.System(), Fixture: true}, []application.Op{
		{Op: application.OpUpdate, Type: "task", ID: w.task, Version: 1, Data: map[string]any{"internalScore": secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Features[0].Data["internalScore"]) != fmt.Sprint(secret) {
		t.Fatal("the system audience reads private fields")
	}
	leak := func(where string, r resp) {
		t.Helper()
		if strings.Contains(r.Raw, "0.987654321") || strings.Contains(r.Raw, "internalScore\":") {
			t.Errorf("%s leaks the private field: %s", where, r.Raw)
		}
	}
	for _, c := range []*client{w.admin, w.editor, w.viewer, w.alice} {
		leak("read", c.do("GET", "/v1/features/task/"+w.task, nil))
		leak("children", c.do("GET", "/v1/features/"+w.project+"/children", nil))
		leak("query", c.do("POST", "/v1/query", map[string]any{"type": "task"}))
		leak("schema", c.do("GET", "/v1/schema", nil))
		leak("schema type", c.do("GET", "/v1/schema/types/task", nil))
		r := c.do("POST", "/v1/query", map[string]any{"type": "task", "where": map[string]any{"field": "/internalScore", "op": "gt", "value": 0.5}})
		if r.code() != kernel.CodeInvalidQuery || !strings.Contains(r.str("detail"), "unknown field") {
			t.Errorf("filtering on a private field must look unknown: %s", r.Raw)
		}
		r = c.do("POST", "/v1/query", map[string]any{"type": "task", "select": []string{"/internalScore"}})
		if r.code() != kernel.CodeInvalidQuery {
			t.Errorf("selecting a private field: %s", r.Raw)
		}
		leak("update error", c.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"internalScore": 1}, "If-Match", `"2"`))
		leak("transaction", c.do("POST", "/v1/transactions", map[string]any{"ops": []any{
			map[string]any{"op": "update", "type": "task", "id": w.task, "version": 2, "data": map[string]any{"title": "renamed"}},
		}}))
	}
}

func TestProtocolRules(t *testing.T) { eachDriver(t, testProtocolRules) }

func testProtocolRules(t *testing.T, driver string) {
	w := setup(t, driver)
	// 428 without If-Match, 409 with a stale one (spec 6.1, 6.6).
	r := w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "x"})
	if r.Status != 428 || r.code() != kernel.CodeVersionRequired {
		t.Fatalf("missing If-Match: %s", r.Raw)
	}
	r = w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "x"}, "If-Match", `"9"`)
	if r.Status != 409 || r.code() != kernel.CodeVersionConflict {
		t.Fatalf("stale If-Match: %s", r.Raw)
	}
	// Validation errors list paths (spec 4.9).
	r = w.admin.create("task", w.project, map[string]any{"title": strings.Repeat("x", 300), "estimate": "1,5"})
	if r.Status != 400 || len(r.Body["errors"].([]any)) != 2 {
		t.Fatalf("validation: %s", r.Raw)
	}
	// Transactions: one failure rejects all; the error names the operation.
	r = w.admin.do("POST", "/v1/transactions", map[string]any{"ops": []any{
		map[string]any{"op": "create", "type": "task", "parent": w.project, "ref": "$a", "data": map[string]any{"title": "a"}},
		map[string]any{"op": "create", "type": "comment", "parent": "$a", "data": map[string]any{}},
	}})
	if r.Status != 400 || r.Body["operation"] != float64(1) {
		t.Fatalf("transaction error: %s", r.Raw)
	}
	// Idempotency keys replay the stored response (spec 6.1).
	first := w.admin.do("POST", "/v1/features/task?parent="+w.project, map[string]any{"title": "once"}, "Idempotency-Key", "k1")
	second := w.admin.do("POST", "/v1/features/task?parent="+w.project, map[string]any{"title": "once"}, "Idempotency-Key", "k1")
	if first.str("id") == "" || first.str("id") != second.str("id") || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("idempotency: %s / %s", first.Raw, second.Raw)
	}
	// Cursors are signed: a tampered cursor is rejected.
	for i := 0; i < 3; i++ {
		mustOK(t, w.admin.create("comment", w.task, map[string]any{"body": fmt.Sprint(i)}), 201)
	}
	page := mustOK(t, w.admin.do("GET", "/v1/features/"+w.task+"/children?limit=2", nil), 200)
	next := page.str("next")
	if next == "" {
		t.Fatal("expected a next cursor")
	}
	page2 := mustOK(t, w.admin.do("GET", "/v1/features/"+w.task+"/children?limit=2&cursor="+next, nil), 200)
	if len(page2.Body["items"].([]any)) != 2 {
		t.Fatalf("second page: %s", page2.Raw)
	}
	bad := w.admin.do("GET", "/v1/features/"+w.task+"/children?limit=2&cursor=x"+next[1:], nil)
	if bad.Status != 400 {
		t.Fatalf("tampered cursor accepted: %s", bad.Raw)
	}
	// Unauthenticated tokens are 401; login failures are generic.
	bogus := &client{s: w.s, token: "fwk_nope"}
	if r := bogus.do("GET", "/v1/features/task/"+w.task, nil); r.Status != 401 {
		t.Fatalf("bogus token: %s", r.Raw)
	}
	r = w.anon.do("POST", "/v1/auth/password", map[string]any{"login": "nobody@example.com", "password": "x", "cli": true})
	r2 := w.anon.do("POST", "/v1/auth/password", map[string]any{"login": adminEmail, "password": "wrong", "cli": true})
	if r.Status != 401 || r2.Status != 401 || r.str("detail") != r2.str("detail") {
		t.Fatalf("login failures must be indistinguishable: %s / %s", r.Raw, r2.Raw)
	}
}
