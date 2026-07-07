package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

// TestDNB_MatchFetchOAuth2 drives the D&B AsyncHTTPAdapter end-to-end through the ADR-0024 stack:
// oauth2-cc token exchange (Phase 2) + match→fetch (Phase 3), with the egress AuthInjector placing
// the Bearer on both round-trips. It asserts the resolved firmographics incl. the genuine DUNS.
func TestDNB_MatchFetchOAuth2(t *testing.T) {
	var tokenHits, matchHits, dataHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		if r.Method != http.MethodPost {
			t.Errorf("token method = %s, want POST", r.Method)
		}
		_, _ = w.Write([]byte(`{"access_token":"TOK-1","expiresIn":86400}`))
	})
	mux.HandleFunc("/v1/match/cleanseMatch", func(w http.ResponseWriter, r *http.Request) {
		matchHits++
		if got := r.Header.Get("Authorization"); got != "Bearer TOK-1" {
			t.Errorf("match call missing injected bearer, got %q", got)
		}
		if got := r.URL.Query().Get("name"); got != "Acme" {
			t.Errorf("match name param = %q, want Acme", got)
		}
		_, _ = w.Write([]byte(`{"matchCandidates":[{"organization":{"duns":"804735132"}}]}`))
	})
	mux.HandleFunc("/v1/data/duns/804735132", func(w http.ResponseWriter, r *http.Request) {
		dataHits++
		if got := r.Header.Get("Authorization"); got != "Bearer TOK-1" {
			t.Errorf("data call missing injected bearer, got %q", got)
		}
		_, _ = w.Write([]byte(`{
			"organization":{
				"duns":"804735132",
				"primaryName":"Acme Manufacturing Company, Inc.",
				"websiteAddress":[{"domainName":"acme.com"}],
				"primaryAddress":{"addressCountry":{"isoAlpha2Code":"US"},"addressLocality":{"name":"San Francisco"}},
				"numberOfEmployees":[{"value":153}],
				"financials":[{"yearlyRevenue":[{"value":51867142,"currency":"USD"}]}],
				"primaryIndustryCode":{"usSicV4":"2752","usSicV4Description":"Commercial printing, lithographic"}
			}
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"dnb:default": "cid:csecret"})

	a := adapters.DNB(srv.URL, client)
	req := provider.Request{
		Known:  map[domain.Field]string{domain.FieldCompanyName: "Acme", domain.FieldCompanyHQCountry: "US"},
		Fields: []domain.Field{domain.FieldDUNS, domain.FieldCompanyName},
	}
	res, err := a.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	want := map[domain.Field]string{
		domain.FieldDUNS:             "804735132",
		domain.FieldCompanyName:      "Acme Manufacturing Company, Inc.",
		domain.FieldCompanyDomain:    "acme.com",
		domain.FieldCompanyHQCountry: "US",
		domain.FieldCompanyHQCity:    "San Francisco",
		domain.FieldEmployeeCount:    "153",
		domain.FieldCompanyRevenue:   "51867142",
		domain.FieldSIC:              "2752",
		domain.FieldIndustry:         "Commercial printing, lithographic",
	}
	for f, v := range want {
		if got := res.Values[f].Value; got != v {
			t.Errorf("%s = %q, want %q", f, got, v)
		}
	}
	if tokenHits != 1 {
		t.Errorf("token exchanged %d times, want 1 (cached across match+fetch)", tokenHits)
	}
	if matchHits != 1 || dataHits != 1 {
		t.Errorf("round-trips: match=%d data=%d, want 1 each", matchHits, dataHits)
	}
}

// TestDNB_NoMatch proves an empty cleanseMatch → NOT_FOUND (engine refunds + fails over), and the
// data-block endpoint is never hit.
func TestDNB_NoMatch(t *testing.T) {
	var dataHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"TOK-1","expiresIn":86400}`))
	})
	mux.HandleFunc("/v1/match/cleanseMatch", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"matchCandidates":[]}`))
	})
	mux.HandleFunc("/v1/data/duns/", func(w http.ResponseWriter, r *http.Request) {
		dataHits++
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"dnb:default": "cid:csecret"})

	a := adapters.DNB(srv.URL, client)
	_, err := a.Fetch(context.Background(), provider.Request{
		Known:  map[domain.Field]string{domain.FieldCompanyName: "Nonexistent"},
		Fields: []domain.Field{domain.FieldDUNS},
	})
	if err == nil {
		t.Fatal("expected NOT_FOUND on empty match, got nil")
	}
	if domain.ClassOf(err) != domain.ClassNotFound {
		t.Errorf("class = %s, want NOT_FOUND", domain.ClassOf(err))
	}
	if dataHits != 0 {
		t.Errorf("data-block endpoint hit %d times on no-match, want 0", dataHits)
	}
}
