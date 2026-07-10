package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

func egressWith(resolver provider.StaticKeyResolver) *http.Client {
	return &http.Client{Transport: provider.NewAuthInjector(http.DefaultTransport, resolver)}
}

func TestSearch_Brave_InjectsKeyAndParses(t *testing.T) {
	var gotKey, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"web":{"results":[{"title":"Acme","url":"https://acme.com","description":"Acme makes widgets"}]}}`)
	}))
	defer srv.Close()

	c := NewClient(egressWith(provider.StaticKeyResolver{"brave-search:default": "bk-1"}))
	p := Provider{Slug: "brave-search", BaseURL: srv.URL, Dialect: DialectBrave, Auth: apiKeyHeader("brave-search:default", "X-Subscription-Token")}

	res, err := c.Search(context.Background(), p, Query{Text: "acme corp", Count: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != "bk-1" {
		t.Fatalf("X-Subscription-Token = %q, want bk-1 (egress injection)", gotKey)
	}
	if gotQuery != "acme corp" {
		t.Fatalf("q = %q, want 'acme corp'", gotQuery)
	}
	if len(res.Hits) != 1 || res.Hits[0].URL != "https://acme.com" || res.Hits[0].Snippet != "Acme makes widgets" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestSearch_Tavily_BearerAndParses(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"title":"T","url":"https://t.example","content":"snippet text"}]}`)
	}))
	defer srv.Close()

	c := NewClient(egressWith(provider.StaticKeyResolver{"tavily:default": "tvly-9"}))
	p := Provider{Slug: "tavily", BaseURL: srv.URL, Dialect: DialectTavily, Auth: bearer("tavily:default")}

	res, err := c.Search(context.Background(), p, Query{Text: "who is acme", Count: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer tvly-9" {
		t.Fatalf("auth = %q, want Bearer tvly-9", gotAuth)
	}
	if !strings.Contains(gotBody, `"query":"who is acme"`) || !strings.Contains(gotBody, `"max_results":3`) {
		t.Fatalf("body = %s", gotBody)
	}
	if len(res.Hits) != 1 || res.Hits[0].Snippet != "snippet text" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestSearch_Serper_ApiKeyHeaderAndParses(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-KEY")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"organic":[{"title":"S","link":"https://s.example","snippet":"serp snippet"}]}`)
	}))
	defer srv.Close()

	c := NewClient(egressWith(provider.StaticKeyResolver{"serper:default": "sk-2"}))
	p := Provider{Slug: "serper", BaseURL: srv.URL, Dialect: DialectSerper, Auth: apiKeyHeader("serper:default", "X-API-KEY")}

	res, err := c.Search(context.Background(), p, Query{Text: "acme"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != "sk-2" {
		t.Fatalf("X-API-KEY = %q, want sk-2", gotKey)
	}
	if len(res.Hits) != 1 || res.Hits[0].URL != "https://s.example" {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestSearch_StatusClassification(t *testing.T) {
	cases := []struct {
		code int
		want domain.ErrorClass
	}{
		{http.StatusUnauthorized, domain.ClassAuth},
		{http.StatusTooManyRequests, domain.ClassRateLimit},
		{http.StatusPaymentRequired, domain.ClassQuota},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		c := NewClient(egressWith(provider.StaticKeyResolver{"brave-search:default": "k"}))
		p := Provider{Slug: "brave-search", BaseURL: srv.URL, Dialect: DialectBrave, Auth: apiKeyHeader("brave-search:default", "X-Subscription-Token")}
		_, err := c.Search(context.Background(), p, Query{Text: "x"})
		if err == nil {
			srv.Close()
			t.Fatalf("status %d: expected error", tc.code)
		}
		if got := domain.ClassOf(err); got != tc.want {
			srv.Close()
			t.Fatalf("status %d classified %v, want %v", tc.code, got, tc.want)
		}
		srv.Close()
	}
}

func TestProviders_InclusionStatus(t *testing.T) {
	byslug := map[string]Provider{}
	for _, p := range Providers() {
		byslug[p.Slug] = p
	}
	if byslug["brave-search"].Status != "ACTIVE-CANDIDATE" {
		t.Errorf("brave-search should be ACTIVE-CANDIDATE, got %q", byslug["brave-search"].Status)
	}
	// Serper/Tavily are Google-SERP-derived (crawl provenance) → DEPRIORITIZED (ADR-0009, RI-OI-1).
	for _, slug := range []string{"serper", "tavily"} {
		if byslug[slug].Status != "DEPRIORITIZED" {
			t.Errorf("%s should be DEPRIORITIZED, got %q", slug, byslug[slug].Status)
		}
	}
}
