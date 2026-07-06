package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// serveFixture returns a mock vendor server that replies with the given testdata file.
func serveFixture(t *testing.T, path string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

// TestAdapters_DecodeRecordedFixtures drives each adapter through the egress key-injection
// seam against a mock serving the pinned (representative, UNVERIFIED) fixtures. It makes the
// assumed vendor response shapes a checked-in contract: a fixture that drifts from Decode
// fails here.
func TestAdapters_DecodeRecordedFixtures(t *testing.T) {
	t.Run("hunter_found", func(t *testing.T) {
		srv := serveFixture(t, "testdata/hunter_found.json")
		defer srv.Close()
		a := adapters.Hunter(srv.URL, clientWith(srv, "hunter:default", "HK"))
		res, err := a.Fetch(context.Background(), person())
		if err != nil {
			t.Fatal(err)
		}
		if we := res.Values[domain.FieldWorkEmail]; we.Value != "jane@acme.com" || we.Confidence != 0.95 {
			t.Fatalf("hunter work_email: %+v", we)
		}
		if es := res.Values[domain.FieldEmailStatus]; es.Value != "valid" {
			t.Fatalf("hunter email_status: %+v", es)
		}
	})

	t.Run("hunter_empty_is_no_value_not_error", func(t *testing.T) {
		srv := serveFixture(t, "testdata/hunter_empty.json")
		defer srv.Close()
		a := adapters.Hunter(srv.URL, clientWith(srv, "hunter:default", "HK"))
		res, err := a.Fetch(context.Background(), person())
		if err != nil {
			t.Fatalf("empty data is a successful no-op, not an error: %v", err)
		}
		if _, ok := res.Values[domain.FieldWorkEmail]; ok {
			t.Fatal("empty email must yield NO work_email observation")
		}
	})

	t.Run("prospeo_found", func(t *testing.T) {
		srv := serveFixture(t, "testdata/prospeo_found.json")
		defer srv.Close()
		a := adapters.Prospeo(srv.URL, clientWith(srv, "prospeo:default", "PSK"))
		res, err := a.Fetch(context.Background(), person())
		if err != nil {
			t.Fatal(err)
		}
		if we := res.Values[domain.FieldWorkEmail]; we.Value != "jane@acme.com" || we.Confidence != 0.88 {
			t.Fatalf("prospeo work_email: %+v", we)
		}
	})

	t.Run("twilio_found", func(t *testing.T) {
		srv := serveFixture(t, "testdata/twilio_found.json")
		defer srv.Close()
		a := adapters.Twilio(srv.URL, clientWith(srv, "twilio-lookup:default", "AC:tok"))
		res, err := a.Fetch(context.Background(), person())
		if err != nil {
			t.Fatal(err)
		}
		if ps := res.Values[domain.FieldPhoneStatus]; ps.Value != "valid" || ps.Confidence != 0.95 {
			t.Fatalf("twilio phone_status: %+v", ps)
		}
	})
}

// TestAdapter_EgressSSRFBlocked proves the SSRF egress choke is active ON THE ADAPTER PATH:
// a real adapter driven through NewEgressClient toward a non-HTTPS / non-allow-listed host is
// refused before any connection, and the refusal maps to a non-retryable BAD_REQUEST.
func TestAdapter_EgressSSRFBlocked(t *testing.T) {
	egress := provider.NewEgressClient(
		provider.NewHostAllowList("api.hunter.io"),
		provider.StaticKeyResolver{"hunter:default": "HK"},
	)
	a := adapters.Hunter("http://127.0.0.1:9/email-finder", egress) // http + loopback: doubly refused
	_, err := a.Fetch(context.Background(), person())
	if err == nil {
		t.Fatal("egress to a disallowed host must be refused")
	}
	if !errors.Is(err, provider.ErrSSRFBlocked) {
		t.Fatalf("refusal should wrap ErrSSRFBlocked, got %v", err)
	}
	if domain.ClassOf(err) != domain.ClassBadRequest {
		t.Fatalf("an SSRF refusal is a non-retryable BAD_REQUEST, got %s", domain.ClassOf(err))
	}
}

// TestAdapters_StatusErrorMatrix pins the HTTP-status -> error-class mapping end-to-end
// through a real adapter (the verified, load-bearing half of the vendor contract).
func TestAdapters_StatusErrorMatrix(t *testing.T) {
	cases := []struct {
		status int
		want   domain.ErrorClass
	}{
		{http.StatusUnauthorized, domain.ClassAuth},         // 401
		{http.StatusPaymentRequired, domain.ClassQuota},     // 402 (Prospeo credits)
		{http.StatusForbidden, domain.ClassRateLimit},       // 403 (Hunter throttle)
		{http.StatusNotFound, domain.ClassNotFound},         // 404 (Twilio no-data)
		{http.StatusTooManyRequests, domain.ClassRateLimit}, // 429
		{http.StatusBadRequest, domain.ClassBadRequest},     // 400
		{http.StatusInternalServerError, domain.ClassTransient},
		{http.StatusServiceUnavailable, domain.ClassProviderDown}, // 503
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			a := adapters.Twilio(srv.URL, clientWith(srv, "twilio-lookup:default", "AC:tok"))
			_, err := a.Fetch(context.Background(), person())
			if got := domain.ClassOf(err); got != tc.want {
				t.Fatalf("status %d should map to %s, got %s", tc.status, tc.want, got)
			}
		})
	}
}
