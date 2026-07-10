package research

import (
	"context"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/router"
)

// EngineEnricher adapts the enrichment engine (a router Plan executed by engine.Run under gates
// G1–G5) to the orchestrator's Enricher seam. It turns a research Subject into an EnrichmentRequest
// for the requested canonical Fields and maps the Outcome back to per-Field values + provenance.
//
// Tenant isolation (G1) flows from the ctx Principal exactly as in the enrichment API — this seam
// never sets a tenant_id itself. Cost (G4) is bounded by CostCeiling; the engine's idempotency
// ledger (G2) makes a re-run of the same JobID free.
type EngineEnricher struct {
	Engine           *engine.Engine
	Planner          *router.Planner
	CostCeiling      domain.Credits
	ConfidenceTarget domain.Confidence
	ConfigVersion    string
	JobID            string // optional; defaults to a per-subject id
}

// Enrich runs the waterfall for the requested Fields and returns the filled values.
func (e EngineEnricher) Enrich(ctx context.Context, subject Subject, fields []domain.Field) (map[domain.Field]FieldValue, error) {
	known := map[domain.Field]string{}
	if subject.Domain != "" {
		known[domain.FieldCompanyDomain] = subject.Domain
	}
	if subject.Name != "" {
		known[domain.FieldCompanyName] = subject.Name
	}
	job := e.JobID
	if job == "" {
		job = "research-" + subjectID(subject)
	}
	req := domain.EnrichmentRequest{
		JobID:            job,
		Subject:          domain.Subject{ID: subjectID(subject), Known: known},
		Want:             fields,
		ConfidenceTarget: e.ConfidenceTarget,
		CostCeiling:      e.CostCeiling,
		ConfigVersion:    e.ConfigVersion,
	}
	out, err := e.Engine.Run(ctx, req, e.Planner.Plan(req))
	if err != nil {
		return nil, err
	}
	res := make(map[domain.Field]FieldValue, len(out.Filled))
	for f, fv := range out.Filled {
		res[f] = FieldValue{
			Value:      fv.Value,
			Confidence: float64(fv.Confidence),
			Provider:   fv.Prov.Provider,
			Cost:       fv.Prov.CostCredits,
		}
	}
	return res, nil
}

// subjectID derives a stable record id for the enrichment request from the Subject (domain first,
// then name).
func subjectID(s Subject) string {
	if s.Domain != "" {
		return s.Domain
	}
	return s.Name
}
