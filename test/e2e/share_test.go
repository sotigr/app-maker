package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ADR 0020: a share link reads one feature's subtree and runs the link's
// actions, by header or by the cookie /s/<token> sets (with CSRF), until
// it expires or is revoked; it sees the public view only.
func TestShareLinks(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		share := mustOK(t, w.editor.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{"expiresInDays": 3, "note": "for the customer"}), 200)
		out := share.Body["output"].(map[string]any)
		tok, _ := out["token"].(string)
		if !strings.HasSuffix(out["url"].(string), "/s/"+tok) || len(out["actions"].([]any)) != 1 {
			t.Fatalf("share: %s", share.Raw)
		}
		if r := w.viewer.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{}); r.Status != 403 {
			t.Fatalf("viewers may not share: %s", r.Raw)
		}
		if r := w.editor.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{"expiresInDays": 90}); r.Status != 400 {
			t.Fatalf("beyond maxDays: %s", r.Raw)
		}
		if r := w.editor.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{"actions": []string{"reopen"}}); r.Status != 400 {
			t.Fatalf("actions outside x-shareable: %s", r.Raw)
		}

		link := &client{s: w.s}
		h := []string{"Authorization", "Link " + tok}
		got := mustOK(t, link.do("GET", "/v1/features/task/"+w.task, nil, h...), 200)
		if _, leak := got.Body["data"].(map[string]any)["internalScore"]; leak {
			t.Fatal("private fields leak to links")
		}
		mustOK(t, link.do("GET", "/v1/features/comment/"+w.comment, nil, h...), 200)
		if r := link.do("GET", "/v1/features/project/"+w.project, nil, h...); r.Status != 404 {
			t.Fatalf("the parent is outside the link: %s", r.Raw)
		}
		q := mustOK(t, link.do("POST", "/v1/query", map[string]any{"type": "task"}, h...), 200)
		if items := q.Body["items"].([]any); len(items) != 1 {
			t.Fatalf("queries see only the shared subtree: %s", q.Raw)
		}
		if r := link.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "x"}, append(h, "If-Match", w.taskVersion(t, w.task))...); r.Status != 403 {
			t.Fatalf("links cannot update: %s", r.Raw)
		}
		if r := link.do("POST", "/v1/features/comment?parent="+w.task, map[string]any{"body": "hi"}, h...); r.Status != 403 && r.Status != 404 {
			t.Fatalf("links cannot create: %s", r.Raw)
		}
		if r := link.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{}, h...); r.Status != 403 {
			t.Fatalf("links cannot share: %s", r.Raw)
		}
		started := mustOK(t, link.do("POST", "/v1/actions/task/"+w.task+"/start", map[string]any{}, h...), 200)
		if started.Body["feature"].(map[string]any)["data"].(map[string]any)["status"] != "in_progress" {
			t.Fatalf("start: %s", started.Raw)
		}
		s := mustOK(t, link.do("GET", "/v1/auth/session", nil, h...), 200)
		if p := s.Body["principal"].(map[string]any); p["kind"] != "link" || p["object"] != w.task {
			t.Fatalf("session: %s", s.Raw)
		}

		// /s/<token> sets the cookies and opens the app; unsafe requests
		// with the cookie need the CSRF header.
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := noFollow.Get(w.s.url + "/s/" + tok)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		var linkC, csrfC *http.Cookie
		for _, c := range res.Cookies() {
			switch c.Name {
			case "organon_link":
				linkC = c
			case "organon_csrf":
				csrfC = c
			}
		}
		if res.StatusCode != 303 || res.Header.Get("Location") != "/" || linkC == nil || !linkC.HttpOnly || csrfC == nil {
			t.Fatalf("/s/: %d %v", res.StatusCode, res.Header)
		}
		cookie := []string{"Cookie", "organon_link=" + linkC.Value}
		mustOK(t, link.do("GET", "/v1/features/task/"+w.task, nil, cookie...), 200)
		if r := link.do("POST", "/v1/actions/task/"+w.task+"/start", map[string]any{}, cookie...); r.Status != 403 {
			t.Fatalf("cookie without CSRF: %s", r.Raw)
		}
		bundle := mustOK(t, link.do("GET", "/v1/ui", nil, cookie...), 200)
		if !strings.Contains(bundle.Raw, `"name":"sharedTask"`) || strings.Contains(bundle.Raw, `"name":"projects"`) || bundle.Body["loginRequired"] == true {
			t.Fatalf("link bundle: %s", bundle.Raw)
		}
		if res, _ := noFollow.Get(w.s.url + "/s/" + tok + "x"); res.Header.Get("Location") != "/?link=invalid" {
			t.Fatalf("bad token: %v", res.Header)
		}

		// Revoked by deleting the shareLink (its creator may), then expired.
		linkID := out["id"].(string)
		lr := mustOK(t, w.editor.do("GET", "/v1/features/shareLink/"+linkID, nil), 200)
		if _, leak := lr.Body["data"].(map[string]any)["tokenHash"]; leak {
			t.Fatal("the token hash is system-only")
		}
		mustOK(t, w.editor.do("DELETE", "/v1/features/shareLink/"+linkID, nil, "If-Match", lr.Header.Get("ETag")), 204)
		if r := link.do("GET", "/v1/features/task/"+w.task, nil, h...); r.Status != 401 {
			t.Fatalf("revoked link: %s", r.Raw)
		}
		other := mustOK(t, w.editor.do("POST", "/v1/actions/task/"+w.task+"/share", map[string]any{"expiresInDays": 1}), 200)
		tok2 := other.Body["output"].(map[string]any)["token"].(string)
		mustOK(t, link.do("GET", "/v1/features/task/"+w.task, nil, "Authorization", "Link "+tok2), 200)
		w.s.clock.Advance(25 * time.Hour)
		if r := link.do("GET", "/v1/features/task/"+w.task, nil, "Authorization", "Link "+tok2); r.Status != 401 {
			t.Fatalf("expired link: %s", r.Raw)
		}
	})
}
