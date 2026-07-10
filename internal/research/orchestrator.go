package research

import (
	"context"
	"fmt"
	"time"

	"github.com/enrichment/waterfall/internal/ai"
	"github.com/enrichment/waterfall/internal/domain"
)

// FieldValue is one enriched canonical Field value plus its provenance (from the enrichment engine).
type FieldValue struct {
	Value      string
	Confidence float64
	Provider   string
	Cost       domain.Credits
}

// Enricher fills canonical Fields for a subject — a seam over the enrichment engine (engine.Run).
// The real wiring lands with the persistence/job increments; the orchestrator depends only on this
// interface so it stays unit-testable and the engine's transitive deps stay out of this package.
type Enricher interface {
	Enrich(ctx context.Context, subject Subject, fields []domain.Field) (map[domain.Field]FieldValue, error)
}

// DiscoveryHit is one search result (discovery only — a URL + text, never a Field value).
type DiscoveryHit struct {
	Title   string
	URL     string
	Snippet string
}

// Discoverer runs a discovery search (internal/collect) and reports which provider served it.
type Discoverer interface {
	Discover(ctx context.Context, query string, count int) (hits []DiscoveryHit, provider string, err error)
}

// AITaskResult is an AI agent task outcome: the raw JSON output plus the accounting the Dossier
// records as provenance (source_type=ai_inference).
type AITaskResult struct {
	Raw   []byte
	Model string
	Cost  domain.Credits
}

// AIRunner runs one AI agent task deterministically (backed by internal/ai's cascade). The
// orchestrator — not the model — decides which task runs and with what input (ADR-0026).
type AIRunner interface {
	RunTask(ctx context.Context, task, input string) (AITaskResult, error)
}

// Orchestrator assembles a Dossier by composing the discovery, enrichment, and AI seams in a fixed,
// DETERMINISTIC order (ADR-0026: the orchestrator chooses the steps, never the model). It is pure
// with respect to those seams; persistence (research_* / migration 0015) and job wiring are layered
// on in later increments. A step failure is logged and the assembly continues (best-effort Dossier).
type Orchestrator struct {
	Enricher   Enricher
	Discoverer Discoverer
	AI         AIRunner
	Fields     []domain.Field   // canonical Fields to enrich for the company profile
	now        func() time.Time // injectable clock
}

// NewOrchestrator builds an orchestrator over the three seams with the default company Field set.
func NewOrchestrator(e Enricher, d Discoverer, a AIRunner) *Orchestrator {
	return &Orchestrator{Enricher: e, Discoverer: d, AI: a, Fields: defaultCompanyFields(), now: time.Now}
}

func defaultCompanyFields() []domain.Field {
	return []domain.Field{
		domain.FieldCompanyName, domain.FieldIndustry, domain.FieldEmployeeCount,
		domain.FieldCompanyRevenue, domain.FieldFundingStage, domain.FieldCompanyHQCountry,
		domain.FieldTwitterURL, domain.FieldCompanyTicker, domain.FieldTotalFundingUSD,
	}
}

// Assemble runs the deterministic DAG and returns the best-effort Dossier. Steps, in order:
//
//	(1) discover  — search keywords/pages via internal/collect (source_type=api)
//	(2) enrich    — canonical Fields via the enrichment engine (source_type=api)
//	(3) ai        — company_research summary via the AI cascade (source_type=ai_inference)
//	(4) intent    — read-only; async lane (ADR-0027), so a sync assembly marks it "pending"
//
// Every produced value lands a Source (G5); AI-inferred values are provenance-distinct and never
// fused as facts.
func (o *Orchestrator) Assemble(ctx context.Context, subject Subject) (Dossier, error) {
	now := o.now
	if now == nil {
		now = time.Now
	}
	d := Dossier{
		Subject:        subject,
		CompanyProfile: map[string]string{},
		Firmographics:  map[string]string{},
		CRMReady:       CRMReady{Account: map[string]string{}},
		Confidence:     ConfidenceSection{BySection: map[string]float64{}},
		DataFreshness:  DataFreshness{GeneratedAt: now()},
	}
	log := func(msg string) { d.ProcessingLog = append(d.ProcessingLog, msg) }

	// (1) Discovery.
	if q := discoveryQuery(subject); q != "" && o.Discoverer != nil {
		hits, prov, err := o.Discoverer.Discover(ctx, q, 5)
		if err != nil {
			log("discover: error: " + err.Error())
		} else {
			for _, h := range hits {
				if h.Title != "" {
					d.SearchKeywords = append(d.SearchKeywords, h.Title)
				}
			}
			d.Provenance = append(d.Provenance, Source{Field: "search_keywords", Provider: prov, SourceType: SourceAPI, Confidence: 0.5})
			log(fmt.Sprintf("discover: %d hits via %s", len(hits), prov))
		}
	}

	// (2) Enrichment (canonical Fields).
	if o.Enricher != nil {
		vals, err := o.Enricher.Enrich(ctx, subject, o.Fields)
		if err != nil {
			log("enrich: error: " + err.Error())
		} else {
			var sum float64
			var n int
			for f, v := range vals {
				if v.Value == "" {
					continue
				}
				d.Firmographics[string(f)] = v.Value
				d.CRMReady.Account[string(f)] = v.Value
				d.Provenance = append(d.Provenance, Source{
					Field: string(f), Provider: v.Provider, SourceType: SourceAPI, Cost: v.Cost, Confidence: v.Confidence,
				})
				sum += v.Confidence
				n++
			}
			if name := vals[domain.FieldCompanyName].Value; name != "" {
				d.CompanyProfile["name"] = name
			}
			if n > 0 {
				d.Confidence.BySection["firmographics"] = sum / float64(n)
			}
			log(fmt.Sprintf("enrich: %d fields filled", n))
		}
	}

	// (3) AI company_research — proposes a summary; parsed by the AI layer's typed contract and kept
	// as an ai_inference value (never fused as a fact).
	if o.AI != nil {
		input := "domain=" + subject.Domain + " name=" + d.CompanyProfile["name"]
		r, err := o.AI.RunTask(ctx, string(ai.TaskCompanyResearch), input)
		if err != nil {
			log("ai company_research: error: " + err.Error())
		} else {
			var out ai.CompanyResearchOutput
			if verr := ai.ValidateInto(r.Raw, &out); verr != nil {
				log("ai company_research: invalid output: " + verr.Error())
			} else {
				d.AISummary = out.Summary
				d.SearchKeywords = append(d.SearchKeywords, out.Keywords...)
				d.Provenance = append(d.Provenance, Source{
					Field: "ai_summary", Provider: r.Model, SourceType: SourceAI, Cost: r.Cost, Confidence: 0.4,
				})
				log("ai company_research via " + r.Model)
			}
		}
	}

	// (4) Intent — async lane (ADR-0027); a sync assembly never blocks on a compute.
	d.Intent.Status = "pending"

	d.Confidence.Overall = overallConfidence(d.Confidence.BySection)
	return d, nil
}

func discoveryQuery(s Subject) string {
	if s.Domain != "" {
		return s.Domain
	}
	return s.Name
}

// overallConfidence is the mean of the per-section confidences (0 when none). This is a placeholder
// aggregate; ADR-0005 calibration of the composite lands with the persistence increment.
func overallConfidence(bySection map[string]float64) float64 {
	if len(bySection) == 0 {
		return 0
	}
	var sum float64
	for _, v := range bySection {
		sum += v
	}
	return sum / float64(len(bySection))
}
