package alerts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDeliverHTTP_SSRFBlocked is P6 gate #4 (SSRF half): a webhook target on a private / cloud
// metadata / loopback address is refused by the SSRF-guarded egress client (result blocked -> the
// HTTP layer maps it to 403 egress_blocked). The DEFAULT (production) egress factory is used, so
// this exercises the real guard.
func TestDeliverHTTP_SSRFBlocked(t *testing.T) {
	ctx := context.Background()
	p := notifyPayload{RuleName: "t", Metric: "m", State: "firing", DedupeKey: "d"}
	cases := []string{
		"https://10.0.0.1/hook",              // RFC1918
		"https://192.168.1.10/hook",          // RFC1918
		"https://169.254.169.254/latest/api", // cloud metadata (link-local)
		"https://127.0.0.1/hook",             // loopback
		"http://example.com/hook",            // non-https is refused by the guard too
	}
	for _, url := range cases {
		res := deliverHTTP(ctx, defaultEgress, "webhook", ChannelConfig{URL: url}, p)
		if !res.blocked {
			t.Fatalf("url %q: expected SSRF-blocked, got %+v", url, res)
		}
		if res.ok {
			t.Fatalf("url %q: blocked target must not report ok", url)
		}
	}
}

// TestDeliverHTTP_HappyPathHMAC proves the delivery + HMAC-signing path against a local sink using a
// permissive injected client (the guarded client blocks loopback by design, so the happy-path sink
// uses an explicitly permissive factory — the block path above uses the real guard).
func TestDeliverHTTP_HappyPathHMAC(t *testing.T) {
	secret := "s3cr3t-signing-key"
	var gotSig, gotDedupe string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Waterfall-Signature")
		gotDedupe = r.Header.Get("X-Waterfall-Dedupe")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	factory := func(string) HTTPDoer { return srv.Client() } // permissive: allows the loopback sink
	p := notifyPayload{RuleName: "hunter errors", Metric: "provider.error_rate", State: "firing",
		Value: 0.2, DedupeKey: "corr-123", FiredAt: time.Now().UTC()}
	res := deliverHTTP(context.Background(), factory, "webhook", ChannelConfig{URL: srv.URL, Secret: secret}, p)
	if !res.ok || res.statusCode != http.StatusOK {
		t.Fatalf("delivery failed: %+v", res)
	}
	if gotDedupe != "corr-123" {
		t.Fatalf("correlation header not propagated: %q", gotDedupe)
	}
	want := "sha256=" + hmacHex(secret, gotBody)
	if gotSig != want {
		t.Fatalf("HMAC signature mismatch: got %q want %q", gotSig, want)
	}
}

// TestRenderBody covers the per-kind body shaping.
func TestRenderBody(t *testing.T) {
	p := notifyPayload{RuleName: "r", Metric: "m", State: "firing"}
	if b := renderBody("slack", p); len(b) == 0 || !contains2(b, "text") {
		t.Fatalf("slack body should carry a text field: %s", b)
	}
	if b := renderBody("discord", p); !contains2(b, "content") {
		t.Fatalf("discord body should carry a content field: %s", b)
	}
	if b := renderBody("webhook", p); !contains2(b, "rule_name") {
		t.Fatalf("webhook body should carry the structured payload: %s", b)
	}
}

func contains2(b []byte, sub string) bool {
	return len(b) >= len(sub) && indexOf(string(b), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
