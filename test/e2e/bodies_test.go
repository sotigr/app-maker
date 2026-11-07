package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
)

// received is one request seen by the echo server.
type received struct {
	method, path, contentType, body string
	header                          http.Header
}

// echoServer records requests and answers by path: /xml400 with an XML
// error document, /busy with 503, /greek with Windows-1253 text, anything
// else with an XML ResponseDoc.
func echoServer(t *testing.T) (*httptest.Server, func() []received) {
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b), r.Header.Clone()})
		mu.Unlock()
		switch r.URL.Path {
		case "/xml400":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`<ResponseDoc><response><statusCode>ValidationError</statusCode></response></ResponseDoc>`))
		case "/busy":
			w.WriteHeader(503)
		case "/greek":
			w.Header().Set("Content-Type", "text/plain; charset=windows-1253")
			_, _ = w.Write([]byte{0xC1, 0xC2, 0xC3}) // ΑΒΓ in Windows-1253
		default:
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = w.Write([]byte(`<ResponseDoc><response><invoiceMark>400001234567890</invoiceMark></response></ResponseDoc>`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), got...)
	}
}

// ADR 0025: http.request sends rawBody byte for byte with the flow's
// Content-Type, form bodies URL-encoded (secrets allowed in values), JSON
// bodies as before; 4xx answers are output, 5xx are retried, text
// responses are decoded by charset, and untraced runs keep no body.
func TestHTTPRequestBodies(t *testing.T) {
	srv, got := echoServer(t)
	flow := func(name, in string, retry string) string {
		return fmt.Sprintf(`{"flow":%q,"trigger":{"action":%q,"on":"urn:app:feature:task"},
		  "nodes":{"r":{"node":"http.request",%s"in":%s}},
		  "result":{"status":"nodes.r.out.status","body":"nodes.r.out.body","ctype":"nodes.r.out.headers['Content-Type']"}}`, name, name, retry, in)
	}
	u := srv.URL
	s := startFull(t, "memory", map[string]string{
		"xml.json": flow("xml", `{"method":"'POST'","url":"'`+u+`/SendInvoices'",
		  "headers":{"Content-Type":"'application/xml; charset=utf-8'","aade-user-id":"'${secret:pay}'"},
		  "rawBody":"'<InvoicesDoc>ΑΒΓ ' + feature.title + '</InvoicesDoc>'"}`, ""),
		"plain.json": flow("plain", `{"method":"'POST'","url":"'`+u+`/p'","rawBody":"'a=1'"}`, ""),
		"form.json": flow("form", `{"method":"'POST'","url":"'`+u+`/token'",
		  "form":{"grant_type":"'client_credentials'","scope":"'a b'","client_secret":"'${secret:pay}'","n":"3"}}`, ""),
		"json.json":  flow("json", `{"method":"'POST'","url":"'`+u+`/j'","body":{"title":"feature.title"}}`, ""),
		"jstr.json":  flow("jstr", `{"method":"'POST'","url":"'`+u+`/s'","body":"'<x/>'"}`, ""),
		"bad.json":   flow("bad", `{"method":"'POST'","url":"'`+u+`/xml400'","rawBody":"'<x/>'"}`, ""),
		"busy.json":  flow("busy", `{"method":"'POST'","url":"'`+u+`/busy'","rawBody":"'<x/>'"}`, `"retry":{"max":2,"backoff":"fixed"},`),
		"greek.json": flow("greek", `{"url":"'`+u+`/greek'"}`, ""),
	}, func(m *manifestdomain.Manifest) {
		m.App.Secrets = map[string]manifestdomain.SecretConfig{"pay": {Hosts: []string{"127.0.0.1"}}}
	}, nil)
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "Τιμή"}), 201).str("id")
	run := func(action string) resp {
		return admin.do("POST", "/v1/actions/task/"+task+"/"+action, map[string]any{})
	}
	last := func() received { g := got(); return g[len(g)-1] }

	r := mustOK(t, run("xml"), 200)
	if l := last(); l.body != "<InvoicesDoc>ΑΒΓ Τιμή</InvoicesDoc>" || l.contentType != "application/xml; charset=utf-8" || l.header.Get("aade-user-id") != "sk_live_SECRETVALUE" {
		t.Fatalf("raw body: %q %q %v", l.body, l.contentType, l.header)
	}
	if !strings.Contains(r.Raw, `\u003cinvoiceMark\u003e400001234567890`) || !strings.Contains(r.Raw, `"status":200`) {
		t.Fatalf("XML response must come back as text: %s", r.Raw)
	}

	mustOK(t, run("plain"), 200)
	if l := last(); l.body != "a=1" || l.contentType != "text/plain; charset=utf-8" {
		t.Fatalf("raw body default type: %q %q", l.body, l.contentType)
	}

	mustOK(t, run("form"), 200)
	if l := last(); l.body != "client_secret=sk_live_SECRETVALUE&grant_type=client_credentials&n=3&scope=a+b" || l.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("form: %q %q", l.body, l.contentType)
	}

	mustOK(t, run("json"), 200)
	if l := last(); l.body != `{"title":"Τιμή"}` || l.contentType != "application/json" {
		t.Fatalf("json body changed: %q %q", l.body, l.contentType)
	}
	mustOK(t, run("jstr"), 200)
	if l := last(); l.body != `"\u003cx/\u003e"` || l.contentType != "application/json" {
		t.Fatalf("json string body changed: %q %q", l.body, l.contentType)
	}

	r = mustOK(t, run("bad"), 200)
	if !strings.Contains(r.Raw, `"status":400`) || !strings.Contains(r.Raw, "ValidationError") {
		t.Fatalf("a 400 answer is output: %s", r.Raw)
	}

	before := len(got())
	if r := run("busy"); r.Status < 400 || len(got())-before != 3 {
		t.Fatalf("503 must be retried (3 attempts): %d %s, %d requests", r.Status, r.Raw, len(got())-before)
	}

	r = mustOK(t, run("greek"), 200)
	if !strings.Contains(r.Raw, `"body":"ΑΒΓ"`) {
		t.Fatalf("charset decoding: %s", r.Raw)
	}

	runs := mustOK(t, admin.do("POST", "/v1/query", map[string]any{"type": "flowRun", "limit": 50}), 200)
	if strings.Contains(runs.Raw, "InvoicesDoc") || strings.Contains(runs.Raw, "client_credentials") || strings.Contains(runs.Raw, "SECRETVALUE") {
		t.Fatalf("untraced runs must not keep request bodies: %s", runs.Raw)
	}
}

// ADR 0025: ctx.http sends raw_body as is, adds several secrets through an
// auth list (headers and a form field), and returns text responses.
func TestPythonHTTPRawBodyAndAuthList(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	srv, got := echoServer(t)
	code := `
import xml.etree.ElementTree as ET

def run(input, ctx):
    r = ctx.http("POST", input["url"] + "/SendInvoices", raw_body="<InvoicesDoc>ΑΒΓ</InvoicesDoc>",
                 headers={"Content-Type": "application/xml; charset=utf-8"},
                 auth=[{"secret": "mydata-user", "header": "aade-user-id"},
                       {"secret": "mydata-key", "header": "ocp-apim-subscription-key"}])
    mark = ET.fromstring(r["body"]).find("response/invoiceMark").text
    t = ctx.http("POST", input["url"] + "/token", form={"grant_type": "client_credentials", "scope": "a b"},
                 auth={"secret": "mydata-key", "form": "client_secret"})
    return {"mark": mark, "status": t["status"]}
`
	flow := `{"flow":"send","trigger":{"action":"send","on":"urn:app:feature:task"},
	  "nodes":{"b":{"node":"python","code":"nodes/send.py","secrets":["mydata-user","mydata-key"],"in":{"url":"'` + srv.URL + `'"},
	    "inputSchema":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]},
	    "outputSchema":{"type":"object","properties":{"mark":{"type":"string"},"status":{"type":"integer"}},"required":["mark","status"]}}},
	  "result":{"mark":"nodes.b.out.mark","status":"nodes.b.out.status"}}`
	s := startFull(t, "memory", map[string]string{"send.json": flow}, func(m *manifestdomain.Manifest) {
		m.App.Secrets = map[string]manifestdomain.SecretConfig{"mydata-user": {Hosts: []string{"127.0.0.1"}}, "mydata-key": {Hosts: []string{"127.0.0.1"}}}
		m.Nodes = append(m.Nodes, manifestdomain.File{Path: "nodes/send.py", Data: []byte(code)})
	}, nil)
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")
	r := mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/send", map[string]any{}), 200)
	if !strings.Contains(r.Raw, `"mark":"400001234567890"`) {
		t.Fatalf("ctx.http: %s", r.Raw)
	}
	g := got()
	if len(g) != 2 {
		t.Fatalf("want 2 requests, got %d", len(g))
	}
	x, f := g[0], g[1]
	if x.body != "<InvoicesDoc>ΑΒΓ</InvoicesDoc>" || x.contentType != "application/xml; charset=utf-8" ||
		x.header.Get("aade-user-id") != "user-SECRETVALUE" || x.header.Get("ocp-apim-subscription-key") != "sk_live_SECRETVALUE" {
		t.Fatalf("raw body request: %+v", x)
	}
	if f.body != "client_secret=sk_live_SECRETVALUE&grant_type=client_credentials&scope=a+b" || f.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("form request: %+v", f)
	}
}
