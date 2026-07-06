package adapters_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// Wave-0 adapters (the "Recommended Starting Stack") are exercised here by a table-driven
// fixture-decode + egress-injection test: each pinned (UNVERIFIED) fixture is served through the
// real adapter + AuthInjector, and the decoded canonical Fields are asserted. A fixture that
// drifts from Decode, or a mapping regression, fails the build. The shared HTTP-status->error-class
// mapping is proven once in TestAdapters_StatusErrorMatrix (live_smoke_test.go); per-provider
// status quirks get their own assertion below.
func TestWave0_DecodeFixtures(t *testing.T) {
	cases := []struct {
		name    string
		newA    func(string, *http.Client) *provider.HTTPAdapter
		pool    string
		fixture string
		req     provider.Request
		// want maps a canonical Field to its expected decoded value; every listed Field must be
		// present with confidence > 0.
		want map[domain.Field]string
	}{
		{
			name:    "people-data-labs",
			newA:    adapters.PeopleDataLabs,
			pool:    "people-data-labs:default",
			fixture: "testdata/people-data-labs_found.json",
			req:     person(),
			want: map[domain.Field]string{
				domain.FieldWorkEmail:     "jane@acme.com",
				domain.FieldMobilePhone:   "+15555550100",
				domain.FieldJobTitle:      "vp sales",
				domain.FieldCompanyName:   "Acme",
				domain.FieldCompanyDomain: "acme.com",
				domain.FieldIndustry:      "software",
				domain.FieldEmployeeCount: "1001-5000",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveFixture(t, tc.fixture)
			defer srv.Close()
			a := tc.newA(srv.URL, clientWith(srv, tc.pool, "SECRET"))
			res, err := a.Fetch(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("%s fetch: %v", tc.name, err)
			}
			for f, wantVal := range tc.want {
				got, ok := res.Values[f]
				if !ok {
					t.Errorf("%s: missing %s", tc.name, f)
					continue
				}
				if got.Value != wantVal {
					t.Errorf("%s: %s = %q, want %q", tc.name, f, got.Value, wantVal)
				}
				if got.Confidence <= 0 || got.Confidence > 1 {
					t.Errorf("%s: %s confidence %v out of (0,1]", tc.name, f, got.Confidence)
				}
			}
		})
	}
}
