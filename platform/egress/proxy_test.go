package egress

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Spec 11.3: code reaches only its network allowlist through the proxy.
func TestProxyAllowlist(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hi") }))
	defer up.Close()
	_, port, _ := net.SplitHostPort(up.Listener.Addr().String())

	p, err := NewProxy([]string{"127.0.0.1:" + port, "*.example.com:443", "api.test:*"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, c := range []struct {
		host, port string
		want       bool
	}{{"127.0.0.1", port, true}, {"127.0.0.1", "1", false}, {"a.example.com", "443", true},
		{"example.com", "443", false}, {"API.test", "8443", true}, {"other.test", "80", false}} {
		if got := p.Allowed(c.host, c.port); got != c.want {
			t.Errorf("Allowed(%s, %s) = %v", c.host, c.port, got)
		}
	}
	pu, _ := url.Parse(p.URL())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	res, err := client.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || string(body) != "hi" {
		t.Fatalf("allowed request: %d %s", res.StatusCode, body)
	}
	res, err = client.Get("http://127.0.0.1:1/")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("denied request: %d", res.StatusCode)
	}

	// Without allowPrivate, allowlisted loopback addresses are still refused.
	strict, err := NewProxy([]string{"127.0.0.1:" + port}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = strict.Close() }()
	su, _ := url.Parse(strict.URL())
	res, err = (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(su)}}).Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("private destination: %d", res.StatusCode)
	}
}
