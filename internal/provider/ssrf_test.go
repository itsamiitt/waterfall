package provider

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSSRF_IPBlocklistCorpus is the internal-range corpus (docs/13 §2.3, docs/21). Every
// blocked address must be refused; a public address must pass.
func TestSSRF_IPBlocklistCorpus(t *testing.T) {
	blocked := []string{
		"169.254.169.254",  // AWS/GCP metadata (link-local)
		"169.254.170.2",    // ECS metadata
		"127.0.0.1",        // loopback
		"127.0.0.53",       // loopback
		"10.0.0.5",         // RFC1918
		"172.16.5.4",       // RFC1918
		"192.168.1.1",      // RFC1918
		"100.64.0.1",       // CGNAT
		"100.127.255.255",  // CGNAT upper
		"0.0.0.0",          // unspecified / 0.0.0.0/8
		"0.1.2.3",          // 0.0.0.0/8
		"::1",              // IPv6 loopback
		"fc00::1",          // IPv6 ULA
		"fd00:ec2::254",    // IPv6 ULA (metadata)
		"fe80::1",          // IPv6 link-local
		"::ffff:127.0.0.1", // IPv4-mapped loopback (encoding bypass attempt)
		"::ffff:10.0.0.1",  // IPv4-mapped RFC1918
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test IP %q failed to parse", s)
		}
		if !isBlockedIP(ip) {
			t.Errorf("SSRF HOLE: %s should be blocked but was allowed", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1::"}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("false positive: public IP %s should be allowed", s)
		}
	}

	// A nil / unparseable IP fails closed.
	if !isBlockedIP(nil) {
		t.Error("nil IP must fail closed (blocked)")
	}
}

// TestSSRF_DialControlBlocksInternal proves the dial-time guard: a real connection attempt
// to an internal IP is refused (this is the DNS-rebinding-safe enforcement point). We hit a
// loopback httptest server through the egress transport directly.
func TestSSRF_DialControlBlocksInternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close() // srv.URL is http://127.0.0.1:PORT

	tr := NewEgressTransport()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("dial to loopback must be blocked at the IP guard, got %v", err)
	}
}

// stubRT lets us assert whether the inner transport was reached.
type stubRT struct{ reached bool }

func (s *stubRT) RoundTrip(*http.Request) (*http.Response, error) {
	s.reached = true
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
}

func TestSSRF_HostGuardEnforcesHTTPSAndAllowList(t *testing.T) {
	allow := NewHostAllowList("api.hunter.io")

	// Non-https is blocked before reaching the inner transport.
	inner := &stubRT{}
	g := &hostGuard{allow: allow, inner: inner}
	req, _ := http.NewRequest(http.MethodGet, "http://api.hunter.io/v2/x", nil)
	if _, err := g.RoundTrip(req); !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("http scheme must be blocked, got %v", err)
	}
	if inner.reached {
		t.Fatal("inner transport must not be reached for a blocked request")
	}

	// Host not on the allow-list is blocked (even over https).
	inner2 := &stubRT{}
	g2 := &hostGuard{allow: allow, inner: inner2}
	req2, _ := http.NewRequest(http.MethodGet, "https://evil.example.com/x", nil)
	if _, err := g2.RoundTrip(req2); !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("disallowed host must be blocked, got %v", err)
	}

	// Allowed https host passes through.
	inner3 := &stubRT{}
	g3 := &hostGuard{allow: allow, inner: inner3}
	req3, _ := http.NewRequest(http.MethodGet, "https://api.hunter.io/v2/x", nil)
	if _, err := g3.RoundTrip(req3); err != nil {
		t.Fatalf("allowed host should pass, got %v", err)
	}
	if !inner3.reached {
		t.Fatal("allowed request should reach the inner transport")
	}
}

// TestSSRF_EgressClientRefusesDisallowedHost exercises the full client wiring.
func TestSSRF_EgressClientRefusesDisallowedHost(t *testing.T) {
	client := NewEgressClient(NewHostAllowList("api.hunter.io"), StaticKeyResolver{})
	// A non-https, non-allowed target must be refused at the client boundary.
	_, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Fatalf("egress client must refuse the metadata endpoint, got %v", err)
	}
}
