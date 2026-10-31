// Package egress is the single component for outbound connections made on
// behalf of an app (spec 14.2): it resolves DNS itself, refuses private,
// loopback, link-local, and cloud metadata addresses, re-checks after
// redirects (at most 3), enforces optional domain allowlists, applies
// timeouts, and caps response sizes.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Config configures the egress client.
type Config struct {
	// Allow, when non-empty, lists the only hosts that may be called.
	Allow []string
	// AllowPrivate permits private and loopback addresses. Development and
	// tests only; never set in production.
	AllowPrivate bool
	Timeout      time.Duration
	MaxBytes     int64
}

// ErrBlocked is returned for refused destinations.
var ErrBlocked = errors.New("egress: destination not allowed")

// Client performs outbound HTTP requests under the egress policy.
type Client struct {
	http   *http.Client
	cfg    Config
	dialer *net.Dialer
}

// New returns a client.
func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 5 << 20
	}
	c := &Client{cfg: cfg, dialer: &net.Dialer{Timeout: 5 * time.Second}}
	transport := &http.Transport{
		Proxy:                 nil, // never route through environment proxies
		DialContext:           c.dial,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          20,
	}
	c.http = &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("%w: too many redirects", ErrBlocked)
			}
			return c.checkHost(req.URL.Hostname())
		},
	}
	return c
}

// dial resolves the host itself and connects to the first allowed
// address, so a second resolution cannot swap in a private address.
func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if err := c.checkIP(ip); err != nil {
			continue
		}
		return c.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return nil, fmt.Errorf("%w: %s resolves only to blocked addresses", ErrBlocked, host)
}

var metadata = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}

func (c *Client) checkIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if slices.Contains(metadata, ip) {
		return ErrBlocked
	}
	if c.cfg.AllowPrivate {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return ErrBlocked
	}
	if ip.Is4() && ip.As4()[0] == 100 && ip.As4()[1]&0xc0 == 64 { // 100.64.0.0/10 carrier-grade NAT
		return ErrBlocked
	}
	return nil
}

func (c *Client) checkHost(host string) error {
	if len(c.cfg.Allow) == 0 {
		return nil
	}
	for _, a := range c.cfg.Allow {
		if strings.EqualFold(a, host) || (strings.HasPrefix(a, "*.") && strings.HasSuffix(strings.ToLower(host), strings.ToLower(a[1:]))) {
			return nil
		}
	}
	return fmt.Errorf("%w: host %s is not in the allowlist", ErrBlocked, host)
}

// Do performs the request; the response body is capped at MaxBytes.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return nil, fmt.Errorf("%w: scheme %s", ErrBlocked, req.URL.Scheme)
	}
	if err := c.checkHost(req.URL.Hostname()); err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil {
		if err := c.checkIP(ip); err != nil {
			return nil, err
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	res.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(res.Body, c.cfg.MaxBytes), res.Body}
	return res, nil
}
