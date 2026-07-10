package research

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

type fakeEnricher struct {
	vals map[domain.Field]FieldValue
	err  error
}

func (f fakeEnricher) Enrich(_ context.Context, _ Subject, _ []domain.Field) (map[domain.Field]FieldValue, error) {
	return f.vals, f.err
}

type fakeDiscoverer struct {
	hits     []DiscoveryHit
	provider string
	err      error
}

func (f fakeDiscoverer) Discover(_ context.Context, _ string, _ int) ([]DiscoveryHit, string, error) {
	return f.hits, f.provider, f.err
}

type fakeAI struct {
	raw   []byte
	model string
	cost  domain.Credits
	err   error
}

func (f fakeAI) RunTask(_ context.Context, _, _ string) (AITaskResult, error) {
	return AITaskResult{Raw: f.raw, Model: f.model, Cost: f.cost}, f.err
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

func TestOrchestrator_AssemblesDossierWithProvenance(t *testing.T) {
	e := fakeEnricher{vals: map[domain.Field]FieldValue{
		domain.FieldCompanyName: {Value: "Acme", Confidence: 0.8, Provider: "brandfetch", Cost: 3},
		domain.FieldTwitterURL:  {Value: "https://twitter.com/acme", Confidence: 0.75, Provider: "brandfetch", Cost: 3},
	}}
	disc := fakeDiscoverer{hits: []DiscoveryHit{{Title: "Acme Inc", URL: "https://acme.com"}}, provider: "brave-search"}
	air := fakeAI{raw: []byte(`{"summary":"Acme makes widgets","search_keywords":["widgets"]}`), model: "llama:free"}

	o := NewOrchestrator(e, disc, air)
	o.now = fixedClock()

	d, err := o.Assemble(context.Background(), Subject{Domain: "acme.com"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Firmographics from enrichment.
	if d.Firmographics["company_name"] != "Acme" || d.Firmographics["twitter_url"] != "https://twitter.com/acme" {
		t.Fatalf("firmographics = %+v", d.Firmographics)
	}
	if d.CompanyProfile["name"] != "Acme" {
		t.Fatalf("company_profile.name = %q", d.CompanyProfile["name"])
	}
	// CRM-ready projection mirrors the account fields.
	if d.CRMReady.Account["company_name"] != "Acme" {
		t.Fatalf("crm_ready.account = %+v", d.CRMReady.Account)
	}
	// AI summary is present and provenance-distinct.
	if d.AISummary != "Acme makes widgets" {
		t.Fatalf("ai_summary = %q", d.AISummary)
	}
	// Discovery + AI keywords both flow into search_keywords.
	if !contains(d.SearchKeywords, "Acme Inc") || !contains(d.SearchKeywords, "widgets") {
		t.Fatalf("search_keywords = %v", d.SearchKeywords)
	}
	// Intent is async → pending on a sync assembly (ADR-0027).
	if d.Intent.Status != "pending" {
		t.Fatalf("intent.status = %q, want pending", d.Intent.Status)
	}
	// Provenance carries the right source types; AI is ai_inference, enrichment is api.
	var sawAPIName, sawAI bool
	for _, s := range d.Provenance {
		if s.Field == "company_name" && s.SourceType == SourceAPI {
			sawAPIName = true
		}
		if s.Field == "ai_summary" && s.SourceType == SourceAI {
			sawAI = true
		}
	}
	if !sawAPIName || !sawAI {
		t.Fatalf("provenance missing typed sources: %+v", d.Provenance)
	}
	if d.DataFreshness.GeneratedAt.Unix() != 1_700_000_000 {
		t.Fatalf("generated_at not stamped from the clock")
	}
	if d.Confidence.Overall <= 0 {
		t.Fatalf("overall confidence should be > 0, got %v", d.Confidence.Overall)
	}
}

func TestOrchestrator_DeterministicStepOrder(t *testing.T) {
	o := NewOrchestrator(
		fakeEnricher{vals: map[domain.Field]FieldValue{domain.FieldCompanyName: {Value: "Acme", Confidence: 0.8, Provider: "p"}}},
		fakeDiscoverer{hits: []DiscoveryHit{{Title: "h"}}, provider: "brave-search"},
		fakeAI{raw: []byte(`{"summary":"s"}`), model: "m"},
	)
	o.now = fixedClock()
	d, _ := o.Assemble(context.Background(), Subject{Domain: "acme.com"})
	// The DAG is fixed: discover → enrich → ai (the orchestrator chooses the order, not the model).
	if len(d.ProcessingLog) != 3 ||
		!strings.HasPrefix(d.ProcessingLog[0], "discover:") ||
		!strings.HasPrefix(d.ProcessingLog[1], "enrich:") ||
		!strings.HasPrefix(d.ProcessingLog[2], "ai ") {
		t.Fatalf("processing_log order = %v", d.ProcessingLog)
	}
}

func TestOrchestrator_ResilientToStepErrors(t *testing.T) {
	// Enrichment fails; discovery + AI still assemble a best-effort Dossier with the error logged.
	o := NewOrchestrator(
		fakeEnricher{err: errors.New("provider down")},
		fakeDiscoverer{hits: []DiscoveryHit{{Title: "h"}}, provider: "brave-search"},
		fakeAI{raw: []byte(`{"summary":"still works"}`), model: "m"},
	)
	o.now = fixedClock()
	d, err := o.Assemble(context.Background(), Subject{Domain: "acme.com"})
	if err != nil {
		t.Fatalf("Assemble should not hard-fail on a step error: %v", err)
	}
	if d.AISummary != "still works" {
		t.Fatalf("ai_summary = %q", d.AISummary)
	}
	var sawErr bool
	for _, l := range d.ProcessingLog {
		if strings.Contains(l, "enrich: error: provider down") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("enrich error not logged: %v", d.ProcessingLog)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
