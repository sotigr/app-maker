package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"organon/internal/flow/domain"
	manifestdomain "organon/internal/manifest/domain"
)

// ADR 0019: http.request sends ${secret:...} in headers and the query
// string to the secret's declared hosts only, and run records keep the
// reference, never the value.
func TestSecretsInOutboundRequests(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	flow := func(name, base string) string {
		return fmt.Sprintf(`{"flow":%q,"trigger":{"action":%q,"on":"urn:app:feature:task"},"trace":true,
		  "nodes":{"pay":{"node":"http.request","in":{"url":"'%s/charge?key=${secret:pay}'",
		    "method":"'POST'","headers":{"Authorization":"'Bearer ${secret:pay}'"},"body":{"title":"feature.title"}}}},
		  "result":{"status":"nodes.pay.out.status"}}`, name, name, base)
	}
	other := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	s := startFull(t, "memory", map[string]string{"pay.json": flow("pay", srv.URL), "leak.json": flow("leak", other)},
		func(m *manifestdomain.Manifest) {
			m.App.Secrets = map[string]manifestdomain.SecretConfig{"pay": {Hosts: []string{"127.0.0.1"}}}
		}, nil)
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")

	r := mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/pay", map[string]any{}), 200)
	if !strings.Contains(r.Raw, `"status":200`) || len(got) != 1 {
		t.Fatalf("pay: %s (%d requests)", r.Raw, len(got))
	}
	if got[0].Header.Get("Authorization") != "Bearer sk_live_SECRETVALUE" || got[0].URL.Query().Get("key") != "sk_live_SECRETVALUE" {
		t.Fatalf("secret not substituted: %v %v", got[0].Header, got[0].URL)
	}

	// The same secret to a host it is not declared for is refused, and
	// nothing is sent.
	if r := admin.do("POST", "/v1/actions/task/"+task+"/leak", map[string]any{}); r.Status < 400 || len(got) != 1 {
		t.Fatalf("leak: %d %s (%d requests)", r.Status, r.Raw, len(got))
	}

	runs := mustOK(t, admin.do("POST", "/v1/query", map[string]any{"type": "flowRun", "limit": 50}), 200)
	if strings.Contains(runs.Raw, "SECRETVALUE") || !strings.Contains(runs.Raw, "may not be sent to localhost") {
		t.Fatalf("run records must never hold the value: %s", runs.Raw)
	}
}

// ADR 0019: the inbound webhook accepts the timestamped signature that
// outbound webhook.call deliveries send.
func TestWebhookTimestampedSignature(t *testing.T) {
	w := setup(t, "memory")
	body := `{"project":"` + w.project + `","title":"Signed"}`
	post := func(at int64) int {
		ts := strconv.FormatInt(at, 10)
		req, _ := http.NewRequest("POST", w.s.url+"/v1/hooks/issues", strings.NewReader(body))
		req.Header.Set("X-Organon-Signature", "t="+ts+",v1="+fmt.Sprintf("%x", hmacOf("s3cret", ts+"."+body)))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	now := w.s.clock.Now().Unix()
	if st := post(now - int64(2*domain.SignatureTolerance.Seconds())); st != 401 {
		t.Fatalf("stale signature: %d", st)
	}
	if st := post(now); st != 202 {
		t.Fatalf("fresh signature: %d", st)
	}
}

func hmacOf(secret, data string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(data))
	return m.Sum(nil)
}

// ADR 0019: Python code calls ctx.http with auth naming a declared secret;
// the engine adds it, so the code only sees the response.
func TestPythonHTTPWithSecret(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"balance":42}`))
	}))
	defer srv.Close()
	code := `
import os

def run(input, ctx):
    r = ctx.http("GET", input["url"], auth={"secret": "pay", "header": "X-Api-Key"})
    return {"balance": r["body"]["balance"], "env": any("SECRETVALUE" in v for v in os.environ.values())}
`
	flow := `{"flow":"balance","trigger":{"action":"balance","on":"urn:app:feature:task"},
	  "nodes":{"b":{"node":"python","code":"nodes/balance.py","secrets":["pay"],"in":{"url":"'` + srv.URL + `/b'"},
	    "inputSchema":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]},
	    "outputSchema":{"type":"object","properties":{"balance":{"type":"integer"},"env":{"type":"boolean"}},"required":["balance","env"]}}},
	  "result":{"balance":"nodes.b.out.balance","env":"nodes.b.out.env"}}`
	s := startFull(t, "memory", map[string]string{"balance.json": flow}, func(m *manifestdomain.Manifest) {
		m.App.Secrets = map[string]manifestdomain.SecretConfig{"pay": {Hosts: []string{"127.0.0.1"}}}
		m.Nodes = append(m.Nodes, manifestdomain.File{Path: "nodes/balance.py", Data: []byte(code)})
	}, nil)
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")
	r := mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/balance", map[string]any{}), 200)
	if !strings.Contains(r.Raw, `"balance":42`) || !strings.Contains(r.Raw, `"env":false`) || auth != "sk_live_SECRETVALUE" {
		t.Fatalf("ctx.http: %s (header %q)", r.Raw, auth)
	}
}
