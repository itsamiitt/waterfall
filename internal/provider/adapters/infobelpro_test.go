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

// TestInfobelPRO_PasswordGrant drives the InfobelPRO adapter through the ADR-0024 oauth2 PASSWORD
// grant (TokenStyle "password"): the egress injector exchanges username:password at /api/token for a
// Bearer, injects it on the /api/search call, and the returnFirstPage records decode to firmographics.
func TestInfobelPRO_PasswordGrant(t *testing.T) {
	var tokenHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "password" {
			t.Errorf("grant_type = %q, want password", got)
		}
		if r.Form.Get("username") != "user1" || r.Form.Get("password") != "pass1" {
			t.Errorf("creds not form-encoded: username=%q password=%q", r.Form.Get("username"), r.Form.Get("password"))
		}
		_, _ = w.Write([]byte(`{"access_token":"TOK-IB","token_type":"bearer","expires_in":1799}`))
	})
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer TOK-IB" {
			t.Errorf("search missing injected bearer, got %q", got)
		}
		_, _ = w.Write([]byte(`{"searchId":42,"FirstPageRecords":[{
			"businessName":"Acme Ltd","webDomain":"https://acme.com","phone":"+3222345678",
			"employeesTotal":320,"salesVolume":15000000,"yearStarted":"1998","country":"Belgium","city":"Brussels",
			"legalStatusCodeDescription":"Corporation","InfobelCategories":[{"infobelLabel01":"Software"}]}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"infobelpro:default": "user1:pass1"})

	a := adapters.InfobelPRO(srv.URL, client)
	res, err := a.Fetch(context.Background(), provider.Request{
		Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com"},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	want := map[domain.Field]string{
		domain.FieldCompanyName:        "Acme Ltd",
		domain.FieldCompanyDomain:      "acme.com",
		domain.FieldCompanyPhone:       "+3222345678",
		domain.FieldEmployeeCount:      "320",
		domain.FieldCompanyRevenue:     "15000000",
		domain.FieldCompanyFoundedYear: "1998",
		domain.FieldCompanyHQCountry:   "Belgium",
		domain.FieldCompanyHQCity:      "Brussels",
		domain.FieldCompanyType:        "Corporation",
		domain.FieldIndustry:           "Software",
	}
	for f, v := range want {
		if got := res.Values[f].Value; got != v {
			t.Errorf("%s = %q, want %q", f, got, v)
		}
	}
	if tokenHits != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cached)", tokenHits)
	}
}
