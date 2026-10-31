package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Proxy is an HTTP proxy for code-node workers (spec 11.3): it allows only
// the node's network allowlist ("host:port", "host:*", "*.example.com:443")
// and refuses private and link-local addresses like the egress client.
// It serves CONNECT tunnels and plain HTTP requests in absolute form.
type Proxy struct {
	allow  []string
	client *Client
	ln     net.Listener
	srv    *http.Server
}

// NewProxy listens on a loopback port. allowPrivate is for tests only.
func NewProxy(allow []string, allowPrivate bool) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{allow: allow, client: New(Config{AllowPrivate: allowPrivate, Timeout: 30 * time.Second}), ln: ln}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// URL is the proxy address for HTTP_PROXY and HTTPS_PROXY.
func (p *Proxy) URL() string { return "http://" + p.ln.Addr().String() }

// Close stops the proxy and its tunnels' listener.
func (p *Proxy) Close() error { return p.srv.Close() }

// Allowed reports whether host:port is on the allowlist.
func (p *Proxy) Allowed(host, port string) bool {
	host = strings.ToLower(host)
	for _, a := range p.allow {
		ah, ap, err := net.SplitHostPort(strings.ToLower(a))
		if err != nil || (ap != "*" && ap != port) {
			continue
		}
		if ah == host || (strings.HasPrefix(ah, "*.") && strings.HasSuffix(host, ah[1:])) {
			return true
		}
	}
	return false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() || r.URL.Scheme != "http" {
		http.Error(w, "absolute http URL required", http.StatusBadRequest)
		return
	}
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if !p.Allowed(r.URL.Hostname(), port) {
		http.Error(w, "destination not allowed", http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	res, err := p.client.http.Transport.RoundTrip(out)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrBlocked) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer func() { _ = res.Body.Close() }()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, p.client.cfg.MaxBytes))
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || !p.Allowed(host, port) {
		http.Error(w, "destination not allowed", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	upstream, err := p.client.dial(ctx, "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := fmt.Fprint(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		_ = conn.Close()
		_ = upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, buf); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
	_ = conn.Close()
	_ = upstream.Close()
	<-done
}
