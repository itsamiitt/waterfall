package adapters

import (
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/provider"
)

// This file is the adapter REGISTRY — the single source of truth binding each provider slug
// to its constructor plus the catalog metadata the seeder needs. It is the bridge between the
// two halves of the system (ADR-0023):
//
//   - the in-process engine wires the adapters via All(egressClient);
//   - the dashboard catalog is seeded by introspecting each constructed *provider.HTTPAdapter
//     (its NameV / BaseURL / Auth / Caps) and combining it with the Registered metadata.
//
// Because both paths read this one list, a runtime adapter and its `providers` catalog row can
// never drift. Adding a provider = append ONE Registered entry here + its `<slug>.go` file. No
// init() magic, no codegen — an explicit slice, matching the project's "explicit over dynamic"
// style (engine.New / router.New already take an explicit []Adapter).

// Registered is one entry in the adapter registry.
type Registered struct {
	// Slug is the stable provider id. It MUST equal the constructed adapter's NameV
	// (asserted by TestRegistry_SlugMatchesAdapterName) and is used as the catalog row id
	// and the "<slug>:default" key-pool selector prefix.
	Slug string
	// New constructs the adapter. base=="" selects the production default BaseURL; tests pass
	// an httptest URL. The shared egress client carries key injection + SSRF guard.
	New func(base string, c *http.Client) *provider.HTTPAdapter
	// Category is the spreadsheet pipeline layer, e.g. "identity", "email-find",
	// "email-verify", "phone-find", "phone-validate", "firmographics", "technographics",
	// "intent", "orchestration". Free text (providers.category has no CHECK constraint).
	Category string
	// Status is the ADR-0009 inclusion verdict: "ACTIVE-CANDIDATE" (clean API-first) or
	// "DEPRIORITIZED" (licensed API but public-web/LinkedIn provenance; off by default until a
	// per-provider compliance review). EXCLUDED providers are NOT registered (see docs/03 §6).
	Status string
	// Regions is the coverage hint, e.g. ["global"], ["EU"], ["US"]; seeded into providers.region.
	Regions []string
	// DocsURL is the vendor's API documentation root (provenance for the researched shape).
	DocsURL string
}

// registry is append-only, ordered by pipeline layer then slug.
var registry = []Registered{
	// L1 — Source / identity + firmographics.
	{Slug: "people-data-labs", New: PeopleDataLabs, Category: "identity", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.peopledatalabs.com/docs/reference-person-enrichment-api"},

	// L2 — Email finding.
	{Slug: "hunter", New: Hunter, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://hunter.io/api-documentation/v2"},
	{Slug: "prospeo", New: Prospeo, Category: "email-find", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://docs.prospeo.io/"},

	// L5 — Phone validation.
	{Slug: "twilio-lookup", New: Twilio, Category: "phone-validate", Status: "ACTIVE-CANDIDATE", Regions: []string{"global"}, DocsURL: "https://www.twilio.com/docs/lookup/v2-api"},
}

// Registry returns the append-only list of registered providers.
func Registry() []Registered { return registry }

// All constructs every registered adapter against the shared egress client. This is what the
// enrich binaries wire into engine.New / router.New in place of the old mock slice. One egress
// client serves every adapter: the per-request AuthDescriptor (carried on the request context)
// tells the injector which key pool to lease, so a single injector authenticates all providers.
func All(c *http.Client) []provider.Adapter {
	out := make([]provider.Adapter, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.New("", c))
	}
	return out
}

// Hosts returns the distinct default base-URL hostnames of every registered adapter, for
// building the egress SSRF allow-list (provider.NewHostAllowList). Constructing with a nil
// client is safe here — Hosts only reads BaseURL, it never performs a Fetch.
func Hosts() []string {
	seen := make(map[string]struct{})
	hosts := make([]string, 0, len(registry))
	for _, r := range registry {
		a := r.New("", nil)
		u, err := url.Parse(a.BaseURL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if _, ok := seen[u.Hostname()]; ok {
			continue
		}
		seen[u.Hostname()] = struct{}{}
		hosts = append(hosts, u.Hostname())
	}
	return hosts
}
