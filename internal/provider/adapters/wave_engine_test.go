package adapters_test

import (
	"context"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

// TestNewAdapters_EngineIntegration drives NEW adapters (a firmographics provider filling the
// extended L6/L7 Field vocabulary + an email verifier) through the full Router + Engine + Store
// spine, proving that (a) the ADR-0023 canonical Fields added for firmo/techno (company_name,
// industry, technographics) pass Field.Valid() and are accepted by the G5 provenance store, and
// (b) the router selects the right provider per wanted Field. This complements
// TestAdapters_EngineIntegration (hunter+twilio) with the post-rollout field set.
func TestNewAdapters_EngineIntegration(t *testing.T) {
	clearbitSrv := serveFixture(t, "testdata/clearbit_found.json")
	defer clearbitSrv.Close()
	zbSrv := serveFixture(t, "testdata/zerobounce_found.json")
	defer zbSrv.Close()

	clearbit := adapters.Clearbit(clearbitSrv.URL, clientWith(clearbitSrv, "clearbit:default", "CB"))
	zerobounce := adapters.ZeroBounce(zbSrv.URL, clientWith(zbSrv, "zerobounce:default", "ZB"))

	st := store.NewMemory()
	eng := engine.New(st, []provider.Adapter{clearbit, zerobounce})
	plan := router.New(clearbit, zerobounce)
	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "t1"})

	req := domain.EnrichmentRequest{
		JobID: "jobF",
		Subject: domain.Subject{ID: "cF", Known: map[domain.Field]string{
			domain.FieldCompanyDomain: "acme.com",
			domain.FieldWorkEmail:     "jane@acme.com",
		}},
		Want: []domain.Field{
			domain.FieldCompanyName, domain.FieldIndustry,
			domain.FieldTechnographics, domain.FieldEmailStatus,
		},
		ConfidenceTarget: 0.95, // force it to try every capable provider for each field
		CostCeiling:      500,
		ConfigVersion:    "v1",
	}
	out, err := eng.Run(ctx, req, plan.Plan(req))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if cn := out.Filled[domain.FieldCompanyName]; cn.Prov.Provider != "clearbit" || cn.Value != "Acme" {
		t.Fatalf("company_name not filled by clearbit: %+v", cn)
	}
	// technographics is the ADR-0023 multi-valued Field stored as one normalized value — proving the
	// extended vocabulary survives the router → engine → provenance store round-trip.
	if tg := out.Filled[domain.FieldTechnographics]; tg.Value != "aws_route_53,mongodb,nginx" {
		t.Fatalf("technographics not filled/normalized: %+v", tg)
	}
	if es := out.Filled[domain.FieldEmailStatus]; es.Prov.Provider != "zerobounce" || es.Value != "valid" {
		t.Fatalf("email_status not filled by zerobounce: %+v", es)
	}
	// G5: every written field carries provenance with a non-empty idempotency key.
	for _, f := range []domain.Field{domain.FieldCompanyName, domain.FieldTechnographics, domain.FieldEmailStatus} {
		if out.Filled[f].Prov.IdempotencyKey == "" {
			t.Fatalf("G5: %s written without an idempotency key", f)
		}
	}
}
