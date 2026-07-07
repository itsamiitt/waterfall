package adapters_test

import (
	"net/url"
	"sort"
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
		if r.Slug == "" || (r.New == nil && r.NewAsync == nil) {
			t.Fatalf("registry entry has empty Slug or no constructor (New/NewAsync): %+v", r)
		}
		if r.New != nil && r.NewAsync != nil {
			t.Fatalf("%s: entry sets both New and NewAsync (exactly one expected)", r.Slug)
		}
		if _, dup := seen[r.Slug]; dup {
			t.Fatalf("duplicate slug %q", r.Slug)
		}
		seen[r.Slug] = struct{}{}

		a := r.Construct("", nil)

		// Slug is the stable id used as the catalog row id and key-pool prefix; it MUST equal
		// the adapter's own Name() or provenance/metrics and the catalog would disagree.
		if a.Name() != r.Slug {
			t.Errorf("%s: adapter Name %q != registry Slug %q", r.Slug, a.Name(), r.Slug)
		}

		// ADR-0009: EXCLUDED providers are never registered; only these two verdicts are valid.
		switch r.Status {
		case "ACTIVE-CANDIDATE", "DEPRIORITIZED":
		default:
			t.Errorf("%s: invalid Status %q (want ACTIVE-CANDIDATE or DEPRIORITIZED)", r.Slug, r.Status)
		}

		// Key-pool selector convention "<slug>:<pool>" (rotation.splitSelector) — the seeder
		// creates the "<slug>:default" pool, so the prefix must be the slug.
		if sel := a.AuthDescriptor().KeyPoolSelector; sel != "" && !strings.HasPrefix(sel, r.Slug+":") {
			t.Errorf("%s: KeyPoolSelector %q must be prefixed %q:", r.Slug, sel, r.Slug)
		}

		// Every advertised capability Field must be in the canonical vocabulary, else the
		// router silently drops it (router.Plan matches on Field via provider.Can).
		caps := a.Capabilities()
		if len(caps) == 0 {
			t.Errorf("%s: adapter advertises no capabilities", r.Slug)
		}
		for _, c := range caps {
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
		u, err := url.Parse(a.Base())
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			t.Errorf("%s: default BaseURL %q must be a valid https URL", r.Slug, a.Base())
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

// TestRegistry_HostsCoverAllAdapters proves the egress SSRF allow-list the binaries build from
// adapters.Hosts() admits EVERY registered adapter: a provider whose base host is missing from the
// list — or an oauth2-cc adapter whose TokenURL host is missing — would have all its calls (or its
// token exchange) SSRF-refused at egress and be silently un-callable. The token-host check matters
// because the oauth2 token exchange runs through the same SSRF-checked base transport.
func TestRegistry_HostsCoverAllAdapters(t *testing.T) {
	allow := provider.NewHostAllowList(adapters.Hosts()...)
	for _, r := range adapters.Registry() {
		a := r.Construct("", nil)
		u, err := url.Parse(a.Base())
		if err != nil || u.Hostname() == "" {
			t.Errorf("%s: base %q has no parseable host", r.Slug, a.Base())
			continue
		}
		if !allow.Allowed(u.Hostname()) {
			t.Errorf("%s: base host %q is NOT in the egress allow-list (adapters.Hosts()) — provider would be SSRF-refused", r.Slug, u.Hostname())
		}
		if d := a.AuthDescriptor(); d.Scheme == provider.AuthOAuth2CC && d.TokenURL != "" {
			if tu, err := url.Parse(d.TokenURL); err == nil && tu.Hostname() != "" && !allow.Allowed(tu.Hostname()) {
				t.Errorf("%s: oauth2 token host %q is NOT in the allow-list — token exchange would be SSRF-refused", r.Slug, tu.Hostname())
			}
		}
	}
	// Sanity: the allow-list is a real filter, not permit-all.
	if allow.Allowed("evil.example.com") {
		t.Error("allow-list admitted an unlisted host — it should reject hosts not derived from the registry")
	}
}

// TestRegistry_FieldCoverage builds the canonical-field → provider-count matrix across all registered
// adapters and asserts that EVERY field in the canonical vocabulary has at least one provider — so the
// router can satisfy a request for any Field, and a Field added to the vocabulary without a mapping
// provider fails the build rather than being silently unfillable. The 90-adapter rollout currently
// covers all 33 canonical fields (e.g. funding_stage via crunchbase/coresignal/oceanio; duns_number
// via dnb; intent_* + buying_signal via 6sense). A curated `essential` subset is checked first so a
// core-field regression names the exact field.
func TestRegistry_FieldCoverage(t *testing.T) {
	// The full canonical vocabulary (mirrors internal/domain/field.go; canonicalFields is unexported).
	all := []domain.Field{
		domain.FieldWorkEmail, domain.FieldPersonalEmail, domain.FieldEmailStatus,
		domain.FieldMobilePhone, domain.FieldDirectDial, domain.FieldOfficePhone, domain.FieldPhoneStatus,
		domain.FieldLinkedInURL, domain.FieldJobTitle, domain.FieldSeniority, domain.FieldDepartment,
		domain.FieldCompanyDomain, domain.FieldCompanyName, domain.FieldEmployeeCount, domain.FieldIndustry,
		domain.FieldCompanyRevenue, domain.FieldFundingStage, domain.FieldCompanyFoundedYear,
		domain.FieldCompanyHQCountry, domain.FieldCompanyHQCity, domain.FieldCompanyType,
		domain.FieldCompanyLinkedInURL, domain.FieldCompanyPhone, domain.FieldNAICS, domain.FieldSIC, domain.FieldDUNS,
		domain.FieldTechnographics, domain.FieldIntentTopics, domain.FieldIntentScore, domain.FieldBuyingSignal,
		domain.FieldFirstName, domain.FieldLastName, domain.FieldFullName,
	}

	count := make(map[domain.Field]int, len(all))
	for _, r := range adapters.Registry() {
		for _, c := range r.Construct("", nil).Capabilities() {
			count[c.Field]++
		}
	}

	// Essential fields: the router MUST have at least one provider for each of these.
	essential := []domain.Field{
		domain.FieldWorkEmail, domain.FieldEmailStatus, domain.FieldMobilePhone, domain.FieldPhoneStatus,
		domain.FieldLinkedInURL, domain.FieldJobTitle, domain.FieldCompanyName, domain.FieldCompanyDomain,
		domain.FieldEmployeeCount, domain.FieldIndustry, domain.FieldFirstName, domain.FieldLastName,
		domain.FieldFullName, domain.FieldTechnographics, domain.FieldNAICS, domain.FieldSIC, domain.FieldDUNS,
		domain.FieldCompanyRevenue, domain.FieldCompanyFoundedYear, domain.FieldCompanyHQCountry,
		domain.FieldIntentScore, domain.FieldBuyingSignal, domain.FieldPersonalEmail,
	}
	for _, f := range essential {
		if count[f] == 0 {
			t.Errorf("essential field %q has NO provider — the router can never fill it", f)
		}
	}

	// Every canonical field must have at least one provider — a vocabulary field nothing can fill is
	// a build-failing gap (either add a provider or remove the field + its doc entry).
	var uncovered []string
	for _, f := range all {
		if count[f] == 0 {
			uncovered = append(uncovered, string(f))
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		t.Errorf("canonical fields with NO provider (unfillable by any of %d adapters): %v", len(adapters.Registry()), uncovered)
	}
}
