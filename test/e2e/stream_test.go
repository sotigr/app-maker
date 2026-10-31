package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"app-maker/internal/kernel"
)

type wsClient struct {
	t    *testing.T
	c    *websocket.Conn
	ctx  context.Context
	msgs chan map[string]any
	// backlog keeps messages skipped by next: subscriptions of one
	// connection are served in no particular order.
	backlog []map[string]any
}

func (c *client) ws(t *testing.T) *wsClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, strings.Replace(c.s.url, "http", "ws", 1)+"/v1/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}}})
	if err != nil {
		t.Fatal(err)
	}
	w := &wsClient{t: t, c: conn, ctx: ctx, msgs: make(chan map[string]any, 1000)}
	go func() {
		for {
			var m map[string]any
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				return
			}
			w.msgs <- m
		}
	}()
	return w
}

func (w *wsClient) send(m map[string]any) {
	if err := wsjson.Write(w.ctx, w.c, m); err != nil {
		w.t.Fatal(err)
	}
}

// next waits for a message matching pred; other messages stay queued
// for later calls.
func (w *wsClient) next(what string, pred func(map[string]any) bool) map[string]any {
	w.t.Helper()
	for i, m := range w.backlog {
		if pred(m) {
			w.backlog = append(w.backlog[:i:i], w.backlog[i+1:]...)
			return m
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-w.msgs:
			if pred(m) {
				return m
			}
			w.backlog = append(w.backlog, m)
		case <-timeout:
			w.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (w *wsClient) quiet(d time.Duration, pred func(map[string]any) bool) {
	w.t.Helper()
	for _, m := range w.backlog {
		if pred(m) {
			w.t.Fatalf("unexpected message %v", m)
		}
	}
	deadline := time.After(d)
	for {
		select {
		case m := <-w.msgs:
			if pred(m) {
				w.t.Fatalf("unexpected message %v", m)
			}
		case <-deadline:
			return
		}
	}
}

func is(t, sid string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == t && m["sid"] == sid }
}

// Spec 9.4–9.6: feature subscriptions get a snapshot, then deltas limited
// to readable, requested paths; private fields never stream.
func TestStreamingFeatureSubscription(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		v := w.viewer.ws(t)
		v.send(map[string]any{"t": "subscribe", "sid": "s1", "target": "feature", "id": w.task})
		snap := v.next("snapshot", is("snapshot", "s1"))
		if feats := snap["features"].([]any); len(feats) != 1 {
			t.Fatalf("snapshot: %v", snap)
		}
		mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "Renamed"}, "If-Match", w.taskVersion(t, w.task)), 200)
		d := v.next("title delta", is("delta", "s1"))
		if set := d["set"].(map[string]any); set["/title"] != "Renamed" || d["sseq"].(float64) != 2 {
			t.Fatalf("delta: %v", d)
		}
		// A private-field change produces no delivery for the viewer.
		ver := w.taskVersion(t, w.task)
		_ = ver
		if _, err := w.s.rt.Content.Execute(context.Background(), systemFixture(), updateOp(w.task, w.taskVersionInt(t, w.task), map[string]any{"internalScore": 0.5})); err != nil {
			t.Fatal(err)
		}
		v.quiet(500*time.Millisecond, func(m map[string]any) bool { return m["t"] == "delta" })

		// Requested paths limit deltas (spec 9.4).
		v.send(map[string]any{"t": "subscribe", "sid": "s2", "target": "feature", "id": w.task, "paths": []string{"/progress"}})
		v.next("snapshot s2", is("snapshot", "s2"))
		mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "Again"}, "If-Match", w.taskVersion(t, w.task)), 200)
		v.next("s1 delta", is("delta", "s1"))
		v.quiet(300*time.Millisecond, is("delta", "s2"))
		mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"progress": 40}, "If-Match", w.taskVersion(t, w.task)), 200)
		d = v.next("s2 delta", is("delta", "s2"))
		if d["set"].(map[string]any)["/progress"] != float64(40) {
			t.Fatalf("progress delta: %v", d)
		}
	})
}

// Children and query subscriptions see features enter and leave.
func TestStreamingChildrenAndQuery(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		e := w.editor.ws(t)
		e.send(map[string]any{"t": "subscribe", "sid": "kids", "target": "children", "parent": w.project, "type": "task"})
		e.next("children snapshot", is("snapshot", "kids"))
		e.send(map[string]any{"t": "subscribe", "sid": "todo", "target": "query",
			"query": map[string]any{"type": "task", "where": map[string]any{"field": "/status", "op": "eq", "value": "todo"}}})
		e.next("query snapshot", is("snapshot", "todo"))
		id := w.newTask(t, map[string]any{"title": "Fresh"})
		if m := e.next("child enters", is("delta", "kids")); m["enter"] != true || m["id"] != id {
			t.Fatalf("enter: %v", m)
		}
		e.next("query enters", is("delta", "todo"))
		w.assign(t, id, w.editor)
		mustOK(t, action(w.editor, id, "start", nil), 200)
		if m := e.next("query leave", is("leave", "todo")); m["id"] != id {
			t.Fatalf("leave: %v", m)
		}
		mustOK(t, w.admin.do("DELETE", "/v1/features/task/"+id, nil, "If-Match", w.taskVersion(t, id)), 204)
		e.next("child leaves", is("leave", "kids"))
	})
}

// Spec 9.7: state mode coalesces a burst (maxRate 10/s) but delivers the
// final value; ephemeral values stream without storage.
func TestStreamingStateCoalescingAndEphemeral(t *testing.T) {
	w := setup(t, "memory")
	e := w.editor.ws(t)
	e.send(map[string]any{"t": "subscribe", "sid": "p", "target": "feature", "id": w.task, "paths": []string{"/progress"}})
	e.next("snapshot", is("snapshot", "p"))
	for i := 1; i <= 30; i++ {
		mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"progress": i}, "If-Match", w.taskVersion(t, w.task)), 200)
	}
	got := 0
	for {
		m := e.next("progress", is("delta", "p"))
		got++
		if m["set"].(map[string]any)["/progress"] == float64(30) {
			break
		}
	}
	if got >= 30 {
		t.Fatalf("no coalescing: %d deliveries for 30 updates", got)
	}

	e.send(map[string]any{"t": "subscribe", "sid": "pr", "target": "feature", "id": w.project, "paths": []string{"/presence"}})
	e.next("project snapshot", is("snapshot", "pr"))
	mustOK(t, w.editor.do("POST", "/v1/ephemeral/project/"+w.project, map[string]any{"presence": "vera is here"}), 202)
	// Ephemeral writes need the update policy (spec 9.7).
	if r := w.viewer.do("POST", "/v1/ephemeral/project/"+w.project, map[string]any{"presence": "x"}); r.Status != 403 {
		t.Fatalf("viewer ephemeral write: %s", r.Raw)
	}
	m := e.next("ephemeral", is("delta", "pr"))
	if m["set"].(map[string]any)["/presence"] != "vera is here" || m["ephemeral"] != true {
		t.Fatalf("ephemeral: %v", m)
	}
	// Persisted fields cannot be written as ephemeral.
	if r := w.editor.do("POST", "/v1/ephemeral/project/"+w.project, map[string]any{"title": "x"}); r.Status != 400 {
		t.Fatalf("ephemeral persisted field: %s", r.Raw)
	}
	// Ephemeral values are never stored.
	if strings.Contains(w.admin.do("GET", "/v1/features/project/"+w.project, nil).Raw, "vera is here") {
		t.Fatal("ephemeral value was stored")
	}
}

// Milestone 6: revoking a grant closes the affected subscription within
// 2 seconds (spec 9.9).
func TestStreamingRevokedGrantClosesSubscription(t *testing.T) {
	w := setup(t, "memory")
	v := w.viewer.ws(t)
	v.send(map[string]any{"t": "subscribe", "sid": "s", "target": "feature", "id": w.task})
	v.next("snapshot", is("snapshot", "s"))
	grants := mustOK(t, w.admin.do("GET", "/v1/features/"+w.project+"/children?type=grant", nil), 200)
	var grantID string
	var version float64
	for _, it := range grants.Body["items"].([]any) {
		g := it.(map[string]any)
		if g["data"].(map[string]any)["subject"] == w.viewer.id {
			grantID, version = g["id"].(string), g["version"].(float64)
		}
	}
	start := time.Now()
	mustOK(t, w.admin.do("DELETE", "/v1/features/grant/"+grantID, nil, "If-Match", etagOf(version)), 204)
	m := v.next("forbidden", is("error", "s"))
	if m["code"] != kernel.CodeForbidden || time.Since(start) > 2*time.Second {
		t.Fatalf("revocation: %v after %v", m, time.Since(start))
	}
}

// SSE delivers one subscription per connection with cursor event IDs.
func TestStreamingSSE(t *testing.T) {
	w := setup(t, "memory")
	req, _ := http.NewRequest("GET", w.s.url+"/v1/stream?target=feature&id="+w.task, nil)
	req.Header.Set("Authorization", "Bearer "+w.viewer.token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %s", res.Header.Get("Content-Type"))
	}
	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "event: snapshot") || !strings.Contains(string(buf[:n]), "id: c:") {
		t.Fatalf("sse: %s", buf[:n])
	}
	mustOK(t, w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"title": "via sse"}, "If-Match", w.taskVersion(t, w.task)), 200)
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) && !strings.Contains(got, "via sse") {
		n, err := res.Body.Read(buf)
		if err != nil {
			break
		}
		got += string(buf[:n])
	}
	if !strings.Contains(got, "event: delta") || !strings.Contains(got, "via sse") {
		t.Fatalf("sse delta: %s", got)
	}
}
