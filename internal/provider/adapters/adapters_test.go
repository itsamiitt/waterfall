package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

// clientWith wraps the test server's transport with the egress AuthInjector, so requests
// carry an injected key the adapter never held.
func clientWith(srv *httptest.Server, pool, secret string) *http.Client {
	c := srv.Client()
	c.Transport = provider.NewAuthInjector(c.Transport, provider.StaticKeyResolver{pool: secret})
	return c
}

func person() provider.Request {
	return provider.Request{Known: map[domain.Field]string{
		domain.FieldCompanyDomain: "acme.com",
		domain.FieldFirstName:     "jane",
		domain.FieldLastName:      "doe",
		domain.FieldMobilePhone:   "+15555550100",
	}}
}

func TestHunter_ContractAndInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != "HK-SECRET" {
			t.Errorf("api_key not injected at egress: %q", got)
		}
		if r.URL.Query().Get("domain") != "acme.com" || r.URL.Query().Get("first_name") != "jane" {
			t.Errorf("query params not built: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":{"email":"jane@acme.com","score":95,"verification":{"status":"valid"}}}`))
	}))
	defer srv.Close()

	a := adapters.Hunter(srv.URL, clientWith(srv, "hunter:default", "HK-SECRET"))
	res, err := a.Fetch(context.Background(), person())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if we := res.Values[domain.FieldWorkEmail]; we.Value != "jane@acme.com" || we.Confidence != 0.95 {
		t.Fatalf("work_email mapping wrong: %+v", we)
	}
	if es := res.Values[domain.FieldEmailStatus]; es.Value != "valid" {
		t.Fatalf("email_status mapping wrong: %+v", es)
	}
}

func TestHunter_403IsRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // Hunter throttle quirk
	}))
	defer srv.Close()
	a := adapters.Hunter(srv.URL, clientWith(srv, "hunter:default", "HK"))
	_, err := a.Fetch(context.Background(), person())
	if domain.ClassOf(err) != domain.ClassRateLimit {
		t.Fatalf("Hunter 403 should map to RATE_LIMIT, got %s", domain.ClassOf(err))
	}
}

func TestProspeo_ContractAndInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-KEY"); got != "PSK" {
			t.Errorf("X-KEY not injected: %q", got)
		}
		_, _ = w.Write([]byte(`{"error":false,"response":{"email":"jane@acme.com","email_status":"valid","email_score":88}}`))
	}))
	defer srv.Close()
	a := adapters.Prospeo(srv.URL, clientWith(srv, "prospeo:default", "PSK"))
	res, err := a.Fetch(context.Background(), person())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if we := res.Values[domain.FieldWorkEmail]; we.Value != "jane@acme.com" || we.Confidence != 0.88 {
		t.Fatalf("prospeo work_email mapping wrong: %+v", we)
	}
}

func TestProspeo_402IsQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	a := adapters.Prospeo(srv.URL, clientWith(srv, "prospeo:default", "PSK"))
	_, err := a.Fetch(context.Background(), person())
	if domain.ClassOf(err) != domain.ClassQuota {
		t.Fatalf("Prospeo 402 should map to QUOTA, got %s", domain.ClassOf(err))
	}
}

func TestTwilio_ContractAndBasicAuthInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "AC123" || pass != "tok" {
			t.Errorf("basic auth not injected: user=%q ok=%v", user, ok)
		}
		if r.URL.Query().Get("Fields") != "line_type_intelligence" {
			t.Errorf("Fields param missing: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"valid":true,"line_type_intelligence":{"type":"mobile"}}`))
	}))
	defer srv.Close()
	a := adapters.Twilio(srv.URL, clientWith(srv, "twilio-lookup:default", "AC123:tok"))
	res, err := a.Fetch(context.Background(), person())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if ps := res.Values[domain.FieldPhoneStatus]; ps.Value != "valid" || ps.Confidence != 0.95 {
		t.Fatalf("phone_status mapping wrong: %+v", ps)
	}
}

// TestAdapters_EngineIntegration drives two REAL adapters through the full Router+Engine
// with the egress injector, proving they plug into the correctness-gate spine unchanged.
func TestAdapters_EngineIntegration(t *testing.T) {
	hunterSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"email":"jane@acme.com","score":91,"verification":{"status":"valid"}}}`))
	}))
	defer hunterSrv.Close()
	twilioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"valid":true,"line_type_intelligence":{"type":"mobile"}}`))
	}))
	defer twilioSrv.Close()

	hunter := adapters.Hunter(hunterSrv.URL, clientWith(hunterSrv, "hunter:default", "HK"))
	twilio := adapters.Twilio(twilioSrv.URL, clientWith(twilioSrv, "twilio-lookup:default", "AC:tok"))

	st := store.NewMemory()
	eng := engine.New(st, []provider.Adapter{hunter, twilio})
	plan := router.New(hunter, twilio)
	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "t1"})

	req := domain.EnrichmentRequest{
		JobID: "job1",
		Subject: domain.Subject{ID: "p1", Known: map[domain.Field]string{
			domain.FieldCompanyDomain: "acme.com", domain.FieldFirstName: "jane",
			domain.FieldLastName: "doe", domain.FieldMobilePhone: "+15555550100",
		}},
		Want:             []domain.Field{domain.FieldWorkEmail, domain.FieldPhoneStatus},
		ConfidenceTarget: 0.9,
		CostCeiling:      100,
		ConfigVersion:    "v1",
	}
	out, err := eng.Run(ctx, req, plan.Plan(req))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if we := out.Filled[domain.FieldWorkEmail]; we.Prov.Provider != "hunter" || we.Value != "jane@acme.com" {
		t.Fatalf("work_email not filled by hunter: %+v", we)
	}
	if ps := out.Filled[domain.FieldPhoneStatus]; ps.Prov.Provider != "twilio-lookup" {
		t.Fatalf("phone_status not filled by twilio: %+v", ps)
	}
	if we := out.Filled[domain.FieldWorkEmail]; we.Prov.IdempotencyKey == "" {
		t.Fatal("G5: provenance missing on real-adapter result")
	}
}
