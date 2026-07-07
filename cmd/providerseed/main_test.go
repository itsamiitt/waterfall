package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// TestSeedInputFor_AllRegistered proves the ADR-0023 registry→catalog projection is sound for EVERY
// registered adapter — including those constructed via NewAsync (D&B/Explorium/Endole/Demandbase/
// Snov) and the dual-header / oauth2-cc / api-key-path auth variants, which reach the seeder through
// Registered.Construct → provider.Introspectable. It asserts each adapter yields a well-formed,
// catalog-insertable SeedInput (matching id, valid ADR-0009 status, https base, ≥1 canonical
// capability, a non-empty auth scheme, a display name, and a unit cost). A drift that would fail the
// live `providerseed` UPSERT — a missing base host, a non-canonical cap, an unset status — fails here
// first, without needing a Postgres test DB.
func TestSeedInputFor_AllRegistered(t *testing.T) {
	reg := adapters.Registry()
	if len(reg) < 80 {
		t.Fatalf("expected the full post-rollout registry, got only %d entries", len(reg))
	}
	// The auth schemes the providers.auth_scheme CHECK constraint accepts (migrations 0005 + 0013).
	// Keep this in lockstep with the migration: a new provider.AuthScheme must land here AND in a
	// migration, or catalog seeding fails at runtime with 23514 (as api-key-dual-header did before
	// migration 0013). This test turns that runtime drift into a build failure.
	catalogAuthSchemes := map[string]bool{
		"api-key-header": true, "api-key-query": true, "api-key-path": true,
		"api-key-dual-header": true, "bearer": true, "basic": true, "oauth2-cc": true,
	}

	seen := make(map[string]bool, len(reg))
	for _, r := range reg {
		in := seedInputFor(r)

		if in.ID != r.Slug {
			t.Errorf("%s: SeedInput.ID %q != registry slug", r.Slug, in.ID)
		}
		if seen[in.ID] {
			t.Errorf("duplicate catalog id %q", in.ID)
		}
		seen[in.ID] = true

		switch in.Status {
		case "ACTIVE-CANDIDATE", "DEPRIORITIZED":
		default:
			t.Errorf("%s: status %q not seedable (ADR-0009 allows only ACTIVE-CANDIDATE|DEPRIORITIZED)", r.Slug, in.Status)
		}

		// A missing/invalid base host would make every call to this provider SSRF-refused at egress.
		u, err := url.Parse(in.BaseURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			t.Errorf("%s: base URL %q is not a valid https URL with a host", r.Slug, in.BaseURL)
		}

		if in.AuthScheme == "" {
			t.Errorf("%s: empty auth scheme (every provider must declare how it authenticates)", r.Slug)
		} else if !catalogAuthSchemes[in.AuthScheme] {
			t.Errorf("%s: auth scheme %q is not accepted by the providers.auth_scheme CHECK "+
				"(migrations 0005+0013) — add a migration widening the constraint before shipping it", r.Slug, in.AuthScheme)
		}
		if strings.TrimSpace(in.DisplayName) == "" {
			t.Errorf("%s: empty display name", r.Slug)
		}
		if in.UnitCostCredits == nil {
			t.Errorf("%s: nil unit cost", r.Slug)
		}
		if len(in.Capabilities) == 0 {
			t.Errorf("%s: no capabilities", r.Slug)
		}
		for _, c := range in.Capabilities {
			if !domain.Field(c.Field).Valid() {
				t.Errorf("%s: capability field %q is not canonical", r.Slug, c.Field)
			}
			if c.CostCredits < 0 {
				t.Errorf("%s: capability %q negative cost %d", r.Slug, c.Field, c.CostCredits)
			}
			if c.ExpectedConfidence < 0 || c.ExpectedConfidence > 1 {
				t.Errorf("%s: capability %q confidence %v out of [0,1]", r.Slug, c.Field, c.ExpectedConfidence)
			}
		}
	}
}
