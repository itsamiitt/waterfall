// Package collect is the R&I data-collection layer (ADR-0025): web SEARCH via third-party search
// APIs (and, later, public DATASET lookups) as bounded, cost-metered egress calls.
//
// A search returns DISCOVERY — result URLs + snippets — not canonical Field values, so, exactly like
// the LLM layer (deviation D-1, docs/research-intelligence/04-ai-pipeline.md), it uses a dedicated
// client that reuses the egress machinery (key injection at the boundary via provider.AuthInjector,
// the SSRF choke, the circuit Breaker, the bounded CallPolicy) rather than the Field-shaped
// provider.Adapter contract. A returned URL is discovery ONLY: per the ADR-0025 boundary it may be
// resolved solely by passing its host/identifier to ANOTHER registered provider API — never fetched
// and DOM-parsed. Search Providers are a SEPARATE registry (Providers()), never wired into the
// enrichment engine.
package collect

import (
	"net/url"

	"github.com/enrichment/waterfall/internal/provider"
)

// Dialect is the request/response wire shape of a search API.
type Dialect int

const (
	// DialectBrave: GET /res/v1/web/search?q= with X-Subscription-Token → {web:{results:[{title,url,description}]}}.
	DialectBrave Dialect = iota
	// DialectTavily: POST /search {query} with Bearer auth → {results:[{title,url,content}]}.
	DialectTavily
	// DialectSerper: POST /search {q} with X-API-KEY → {organic:[{title,link,snippet}]}.
	DialectSerper
)

// Provider is one search API — the collection-layer analogue of a catalog row: a stable slug (also
// the key-pool selector prefix), the egress endpoint, the wire Dialect, an AuthDescriptor (so the
// AuthInjector leases + injects the key), and its ADR-0009 inclusion Status. It holds NO secret.
type Provider struct {
	Slug    string
	BaseURL string
	Dialect Dialect
	Auth    provider.AuthDescriptor
	Status  string // ADR-0009 inclusion verdict
	DocsURL string
}

// Providers is the append-only search registry. Brave (own crawl index) is ACTIVE-CANDIDATE;
// Serper/Tavily are Google-SERP-derived (crawl provenance) → DEPRIORITIZED behind the ADR-0009
// human-policy gate until confirmed (RI-OI-1). This is NOT the enrichment adapter registry.
func Providers() []Provider {
	return []Provider{
		{
			Slug: "brave-search", BaseURL: "https://api.search.brave.com/res/v1/web/search",
			Dialect: DialectBrave, Auth: apiKeyHeader("brave-search:default", "X-Subscription-Token"),
			Status: "ACTIVE-CANDIDATE", DocsURL: "https://api-dashboard.search.brave.com/app/documentation/web-search/get-started",
		},
		{
			Slug: "tavily", BaseURL: "https://api.tavily.com/search",
			Dialect: DialectTavily, Auth: bearer("tavily:default"),
			Status: "DEPRIORITIZED", DocsURL: "https://docs.tavily.com/documentation/api-reference/endpoint/search",
		},
		{
			Slug: "serper", BaseURL: "https://google.serper.dev/search",
			Dialect: DialectSerper, Auth: apiKeyHeader("serper:default", "X-API-KEY"),
			Status: "DEPRIORITIZED", DocsURL: "https://serper.dev/playground",
		},
	}
}

// Hosts returns the distinct hostnames of every search Provider's BaseURL, for extending the egress
// SSRF allow-list (provider.NewHostAllowList) — the research orchestrator calls these through the
// same egress client as the enrichment adapters.
func Hosts() []string {
	seen := map[string]struct{}{}
	var hosts []string
	for _, p := range Providers() {
		u, err := url.Parse(p.BaseURL)
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

func bearer(sel string) provider.AuthDescriptor {
	return provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: sel}
}

func apiKeyHeader(sel, header string) provider.AuthDescriptor {
	return provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: header, KeyPoolSelector: sel}
}
