package adapters_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// TestRegistry_Invariants guards EVERY registered adapter (current and future) against the
// contract the engine wiring and the catalog seeder both depend on. A new provider that
// violates any of these fails the build here rather than at runtime.
func TestRegistry_Invariants(t *testing.T) {
	reg := adapters.Registry()
	if len(reg) == 0 {
		t.Fatal("registry is empty")
	}
	seen := make(map[string]struct{}, len(reg))
	for _, r := range reg {
		if r.Slug == "" || r.New == nil {
			t.Fatalf("registry entry has empty Slug or nil New: %+v", r)
		}
		if _, dup := seen[r.Slug]; dup {
			t.Fatalf("duplicate slug %q", r.Slug)
		}
		seen[r.Slug] = struct{}{}

		a := r.New("", nil)

		// Slug is the stable id used as the catalog row id and key-pool prefix; it MUST equal
		// the adapter's own NameV or provenance/metrics and the catalog would disagree.
		if a.NameV != r.Slug {
			t.Errorf("%s: adapter NameV %q != registry Slug %q", r.Slug, a.NameV, r.Slug)
		}

		// ADR-0009: EXCLUDED providers are never registered; only these two verdicts are valid.
		switch r.Status {
		case "ACTIVE-CANDIDATE", "DEPRIORITIZED":
		default:
			t.Errorf("%s: invalid Status %q (want ACTIVE-CANDIDATE or DEPRIORITIZED)", r.Slug, r.Status)
		}

		// Key-pool selector convention "<slug>:<pool>" (rotation.splitSelector) — the seeder
		// creates the "<slug>:default" pool, so the prefix must be the slug.
		if sel := a.Auth.KeyPoolSelector; sel != "" && !strings.HasPrefix(sel, r.Slug+":") {
			t.Errorf("%s: KeyPoolSelector %q must be prefixed %q:", r.Slug, sel, r.Slug)
		}

		// Every advertised capability Field must be in the canonical vocabulary, else the
		// router silently drops it (router.Plan matches on Field via provider.Can).
		if len(a.Caps) == 0 {
			t.Errorf("%s: adapter advertises no capabilities", r.Slug)
		}
		for _, c := range a.Caps {
			if !c.Field.Valid() {
				t.Errorf("%s: capability Field %q is not canonical (extend internal/domain/field.go + docs/00 §7)", r.Slug, c.Field)
			}
			if c.Cost < 0 {
				t.Errorf("%s: capability %q has negative cost %d", r.Slug, c.Field, c.Cost)
			}
			if c.ExpectedConfidence < 0 || c.ExpectedConfidence > 1 {
				t.Errorf("%s: capability %q expected confidence %v out of [0,1]", r.Slug, c.Field, c.ExpectedConfidence)
			}
		}

		// Default BaseURL must be a well-formed https URL (SSRF egress is HTTPS-only).
		u, err := url.Parse(a.BaseURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			t.Errorf("%s: default BaseURL %q must be a valid https URL", r.Slug, a.BaseURL)
		}
	}
}

// TestRegistry_AllConstructs proves All() builds one Adapter per entry and Hosts() yields the
// egress allow-list — the exact wiring the enrich binaries use.
func TestRegistry_AllConstructs(t *testing.T) {
	all := adapters.All(nil)
	if len(all) != len(adapters.Registry()) {
		t.Fatalf("All() returned %d adapters, want %d", len(all), len(adapters.Registry()))
	}
	var _ []provider.Adapter = all // All returns []provider.Adapter

	hosts := adapters.Hosts()
	if len(hosts) == 0 {
		t.Fatal("Hosts() returned no allow-list entries")
	}
	for _, h := range hosts {
		if h == "" || strings.ContainsAny(h, "/: ") {
			t.Errorf("Hosts() entry %q is not a bare hostname", h)
		}
	}

	// Spot-check a known field stays canonical end-to-end.
	if !domain.FieldWorkEmail.Valid() {
		t.Fatal("sanity: work_email should be canonical")
	}
}
