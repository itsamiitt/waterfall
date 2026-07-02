package provider

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// This file is the SSRF choke point (docs/13, docs/18 §2 — the #2 security risk). All
// outbound provider/webhook calls flow through an EgressClient that enforces, in order:
//
//  1. HTTPS-only.
//  2. FQDN allow-list (host must be explicitly permitted; never derived from record data).
//  3. Dial-time IP guard: the ACTUAL resolved IP being connected to is validated against
//     the internal-range blocklist. Because the check is on the resolved binary IP at the
//     moment of dialing (via the dialer Control hook), it is immune to DNS-rebinding
//     (a name that resolves to a public IP for the allow-list check but an internal IP at
//     dial time is still blocked) and to IP-literal encoding tricks.
//  4. Redirects are re-checked against 1–3 on every hop.
//
// Key injection (egress.go) happens at this same boundary — the only place a secret exists.

// ErrSSRFBlocked is returned when the egress policy refuses a request. It is wrapped with
// a specific reason. Adapters classify it as BAD_REQUEST (non-retryable): it is a policy
// refusal, not a transient fault.
var ErrSSRFBlocked = errors.New("egress: blocked by SSRF policy")

// HostAllowList is a set of permitted hostnames (case-insensitive). Provider base hosts are
// a global list; tenant webhook hosts are a per-tenant list (docs/13 §6) — callers build
// the appropriate list for the request's purpose.
type HostAllowList map[string]struct{}

// NewHostAllowList builds an allow-list from hostnames.
func NewHostAllowList(hosts ...string) HostAllowList {
	m := make(HostAllowList, len(hosts))
	for _, h := range hosts {
		m[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return m
}

// Allowed reports whether host is permitted.
func (a HostAllowList) Allowed(host string) bool {
	_, ok := a[strings.ToLower(host)]
	return ok
}

// isBlockedIP reports whether ip is in a range that must never be reachable via egress:
// loopback, RFC1918 + ULA (IsPrivate), link-local incl. cloud metadata 169.254.169.254,
// multicast, unspecified, CGNAT 100.64/10, and 0.0.0.0/8. A nil/unparseable IP is blocked
// (fail closed).
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:a.b.c.d) collapses to a 4-byte form here, so these checks
	// also cover encoded internal addresses.
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 { // 0.0.0.0/8
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 { // 100.64.0.0/10 CGNAT
			return true
		}
	}
	return false
}

// safeControl is the net.Dialer Control hook. It runs after DNS resolution with the
// concrete IP:port about to be dialed, and refuses connections to blocked IPs. This is the
// rebinding-safe enforcement point.
func safeControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: bad dial address %q", ErrSSRFBlocked, address)
	}
	ip := net.ParseIP(host)
	if ip == nil || isBlockedIP(ip) {
		return fmt.Errorf("%w: refused connection to %s", ErrSSRFBlocked, host)
	}
	return nil
}

// NewEgressTransport builds an http.Transport whose dialer validates the resolved IP of
// every connection (SSRF dial guard). It is used on its own only in tests; production code
// uses NewEgressClient which layers the host allow-list and key injection on top.
func NewEgressTransport() *http.Transport {
	d := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   safeControl,
	}
	return &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// hostGuard enforces HTTPS-only + the FQDN allow-list before delegating to the inner
// (key-injecting) transport.
type hostGuard struct {
	allow HostAllowList
	inner http.RoundTripper
}

func (g *hostGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%w: non-https scheme %q", ErrSSRFBlocked, req.URL.Scheme)
	}
	if !g.allow.Allowed(req.URL.Hostname()) {
		return nil, fmt.Errorf("%w: host %q not on allow-list", ErrSSRFBlocked, req.URL.Hostname())
	}
	return g.inner.RoundTrip(req)
}

// NewEgressClient builds the hardened HTTP client used for all outbound calls: HTTPS-only +
// allow-list (hostGuard) → key injection (AuthInjector) → IP-guarded dial (egress
// transport). Redirects are re-validated on every hop and capped.
func NewEgressClient(allow HostAllowList, keys KeyResolver) *http.Client {
	transport := NewEgressTransport()
	injected := NewAuthInjector(transport, keys)
	guarded := &hostGuard{allow: allow, inner: injected}
	return &http.Client{
		Transport: guarded,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("%w: too many redirects", ErrSSRFBlocked)
			}
			// Re-check the redirect target (the request also re-enters hostGuard, but fail
			// fast here with a clear reason).
			if req.URL.Scheme != "https" || !allow.Allowed(req.URL.Hostname()) {
				return fmt.Errorf("%w: redirect to disallowed %q", ErrSSRFBlocked, req.URL)
			}
			return nil
		},
	}
}
