package e2e

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
)

// ADR 0016: GET /v1/ui gives each principal the shell, the pages it may
// see, and metadata of the fields it may read; anonymous callers of an
// app that requires login get only the login shell.
func TestUIBundle(t *testing.T) {
	w := setup(t, "memory")
	anon := mustOK(t, w.anon.do("GET", "/v1/ui", nil), 200)
	if anon.Body["loginRequired"] != true || len(anon.Body["pages"].([]any)) != 0 || len(anon.Body["types"].(map[string]any)) != 0 {
		t.Fatalf("anonymous bundle: %s", anon.Raw)
	}
	if _, ok := anon.Body["app"].(map[string]any)["nav"]; ok {
		t.Fatal("navigation is hidden until login")
	}
	b := mustOK(t, w.editor.do("GET", "/v1/ui", nil), 200)
	var pages []string
	for _, p := range b.Body["pages"].([]any) {
		pages = append(pages, p.(map[string]any)["name"].(string))
	}
	if strings.Join(pages, ",") != "myTasks,project,projects,task" {
		t.Fatalf("pages: %v", pages)
	}
	task := b.Body["types"].(map[string]any)["task"].(map[string]any)
	fields := task["fields"].(map[string]any)
	if _, leak := fields["/internalScore"]; leak {
		t.Fatal("private field metadata must not reach browsers")
	}
	if fields["/assignee"].(map[string]any)["kind"] != "ref" || fields["/status"].(map[string]any)["enum"] == nil {
		t.Fatalf("field metadata: %v", fields)
	}
	if _, ok := task["actions"].(map[string]any)["triage"]; !ok {
		t.Fatalf("flow actions: %v", task["actions"])
	}
	if !strings.HasPrefix(b.str("styles"), "/assets/styles.css?v=") {
		t.Fatalf("styles: %q", b.str("styles"))
	}
	// The nav entry for users is filtered by its visible expression.
	if nav := b.Body["app"].(map[string]any)["nav"].([]any); len(nav) != 2 {
		t.Fatalf("nav: %v", nav)
	}
}

// ADR 0016: the renderer is served at / for every client-side route, with
// a strict CSP; app assets and styles are served with nosniff; API and
// reserved paths never fall back to the page.
func TestUIServing(t *testing.T) {
	s := startFull(t, "memory", nil, func(m *manifestdomain.Manifest) {
		m.UI = append(m.UI, manifestdomain.File{Path: "ui/assets/logo.svg", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)})
	}, nil)
	a := s.anon()
	for _, path := range []string{"/", "/projects/123", "/my-tasks"} {
		r := a.do("GET", path, nil)
		if r.Status != 200 || !strings.Contains(r.Raw, `<div id="root">`) {
			t.Fatalf("%s: %d %s", path, r.Status, r.Raw)
		}
		if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'self'") {
			t.Fatalf("%s csp: %q", path, csp)
		}
	}
	js := regexp.MustCompile(`/assets/organon/[^"]+\.js`).FindString(a.do("GET", "/", nil).Raw)
	if r := a.do("GET", js, nil); js == "" || r.Status != 200 || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("runtime %q: %d", js, r.Status)
	}
	css := a.do("GET", "/assets/styles.css", nil)
	if css.Status != 200 || !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") || css.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("styles: %d %v", css.Status, css.Header)
	}
	svg := a.do("GET", "/assets/app/logo.svg", nil)
	if svg.Status != 200 || svg.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(svg.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("svg asset: %d %v", svg.Status, svg.Header)
	}
	for _, path := range []string{"/assets/app/%2e%2e/app.json", "/assets/app/missing.png", "/assets/app/run.js",
		"/assets/organon/missing.js", "/v1/nope"} {
		if r := a.do("GET", path, nil); r.Status == 200 {
			t.Fatalf("%s must not be served: %s", path, r.Raw)
		}
	}
	// Without ui/, the root goes to the admin console.
	bare := startFull(t, "memory", nil, func(m *manifestdomain.Manifest) { m.UI = nil }, nil)
	req, _ := http.NewRequest("GET", bare.url+"/", nil)
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/admin/" {
		t.Fatalf("no ui: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if r := bare.anon().do("GET", "/projects", nil); r.Status != 404 {
		t.Fatalf("no ui, app route: %d", r.Status)
	}
}

// ADR 0016: POST /v1/permissions answers can(f, op) with the same policy,
// state, and guard checks as the real operations, and never reveals
// features the caller cannot read.
func TestUIPermissions(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		checks := []map[string]any{
			{"id": w.task, "op": "update"},
			{"id": w.task, "op": "delete"},
			{"id": w.task, "op": "action:start"},
			{"id": w.task, "op": "action:complete"},
			{"id": w.task, "op": "create:comment"},
			{"id": w.project, "op": "create:comment"}, // projects do not allow comments
			{"id": w.task, "op": "action:transition"},
			{"id": "not-an-id", "op": "update"},
		}
		ask := func(c *client) string {
			r := mustOK(t, c.do("POST", "/v1/permissions", map[string]any{"checks": checks}), 200)
			var out []string
			for _, v := range r.Body["results"].([]any) {
				if v == true {
					out = append(out, "y")
				} else {
					out = append(out, "n")
				}
			}
			return strings.Join(out, "")
		}
		for name, tc := range map[string]struct {
			c    *client
			want string
		}{
			"admin":    {w.admin, "yyynyny"},
			"editor":   {w.editor, "ynynyny"},
			"viewer":   {w.viewer, "nnnnynn"},
			"stranger": {w.stranger, "nnnnnnn"},
			"anon":     {w.anon, "nnnnnnn"},
		} {
			if got := ask(tc.c); got != tc.want+"n" {
				t.Errorf("%s: got %s, want %sn", name, got, tc.want)
			}
		}
		// complete needs the assignee (guard) and the in_progress state.
		w.assign(t, w.task, w.editor)
		mustOK(t, action(w.editor, w.task, "start", nil), 200)
		if r := mustOK(t, w.editor.do("POST", "/v1/permissions", map[string]any{"checks": []map[string]any{
			{"id": w.task, "op": "action:complete"}, {"id": w.task, "op": "action:start"}}}), 200); !strings.Contains(r.Raw, `[true,false]`) {
			t.Fatalf("after start: %s", r.Raw)
		}
		many := make([]map[string]any, 101)
		for i := range many {
			many[i] = map[string]any{"id": w.task, "op": "update"}
		}
		if r := w.editor.do("POST", "/v1/permissions", map[string]any{"checks": many}); r.Status != 400 {
			t.Fatalf("101 checks: %d", r.Status)
		}
	})
}
