package research

import (
	"context"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

func principalCtx() context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "t1", UserID: "u1"})
}

func TestEngineEnricher_FillsCanonicalFields(t *testing.T) {
	nameP := providertest.New("vendor-name", "Acme", 0.85, 2, domain.FieldCompanyName)
	industryP := providertest.New("vendor-industry", "Software", 0.70, 2, domain.FieldIndustry)

	st := store.NewMemory()
	eng := engine.New(st, []provider.Adapter{nameP, industryP})
	enr := EngineEnricher{
		Engine: eng, Planner: router.New(nameP, industryP),
		CostCeiling: 50, ConfidenceTarget: 0.90, ConfigVersion: "test-v1",
	}

	vals, err := enr.Enrich(principalCtx(), Subject{Domain: "acme.com"},
		[]domain.Field{domain.FieldCompanyName, domain.FieldIndustry})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got := vals[domain.FieldCompanyName]; got.Value != "Acme" || got.Provider != "vendor-name" || got.Confidence <= 0 {
		t.Fatalf("company_name = %+v", got)
	}
	if got := vals[domain.FieldIndustry]; got.Value != "Software" {
		t.Fatalf("industry = %+v", got)
	}
}

// TestOrchestrator_WithRealEngineEnricher runs the orchestrator against the REAL enrichment engine
// (in-memory store + mock providers) + a stub AI runner — proving the enrichment seam composes into
// a Dossier end-to-end without PG.
func TestOrchestrator_WithRealEngineEnricher(t *testing.T) {
	nameP := providertest.New("vendor-name", "Acme", 0.85, 2, domain.FieldCompanyName)
	eng := engine.New(store.NewMemory(), []provider.Adapter{nameP})
	enr := EngineEnricher{
		Engine: eng, Planner: router.New(nameP),
		CostCeiling: 50, ConfidenceTarget: 0.90, ConfigVersion: "v1",
	}

	o := NewOrchestrator(enr, nil, fakeAI{raw: []byte(`{"summary":"ok"}`), model: "m"})
	o.now = fixedClock()

	d, err := o.Assemble(principalCtx(), Subject{Domain: "acme.com"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if d.Firmographics["company_name"] != "Acme" {
		t.Fatalf("firmographics = %+v", d.Firmographics)
	}
	if d.CompanyProfile["name"] != "Acme" {
		t.Fatalf("company_profile.name = %q", d.CompanyProfile["name"])
	}
	if d.AISummary != "ok" {
		t.Fatalf("ai_summary = %q", d.AISummary)
	}
	// Provenance for company_name is source_type=api (from the engine), not ai_inference.
	var sawAPI bool
	for _, s := range d.Provenance {
		if s.Field == "company_name" && s.SourceType == SourceAPI && s.Provider == "vendor-name" {
			sawAPI = true
		}
	}
	if !sawAPI {
		t.Fatalf("expected an api-source provenance row for company_name via vendor-name: %+v", d.Provenance)
	}
}
