package research

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/ai"
	"github.com/enrichment/waterfall/internal/collect"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

func egress(resolver provider.StaticKeyResolver) *http.Client {
	return &http.Client{Transport: provider.NewAuthInjector(http.DefaultTransport, resolver)}
}

// stubCompleter returns a fixed completion for any model (the cascade wraps it deterministically).
type stubCompleter struct{ text string }

func (s stubCompleter) Complete(_ context.Context, m ai.Model, _ ai.CompletionRequest) (ai.Completion, error) {
	return ai.Completion{Text: s.text, Model: m.ModelID}, nil
}

func TestCollectDiscoverer_OverBrave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"web":{"results":[{"title":"Acme Inc","url":"https://acme.com","description":"widgets"}]}}`)
	}))
	defer srv.Close()

	client := collect.NewClient(egress(provider.StaticKeyResolver{"brave-search:default": "k"}))
	p := collect.Provider{Slug: "brave-search", BaseURL: srv.URL, Dialect: collect.DialectBrave,
		Auth: provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-Subscription-Token", KeyPoolSelector: "brave-search:default"}}

	d := CollectDiscoverer{Client: client, Provider: p}
	hits, prov, err := d.Discover(context.Background(), "acme", 3)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if prov != "brave-search" || len(hits) != 1 || hits[0].Title != "Acme Inc" {
		t.Fatalf("hits=%+v prov=%q", hits, prov)
	}
}

func TestCascadeAIRunner_RunsTaskAndValidates(t *testing.T) {
	r := CascadeAIRunner{
		Completer: stubCompleter{text: `{"summary":"Acme makes widgets"}`},
		Models:    []ai.Model{{Slug: "free", ModelID: "free-model", Free: true}},
		Budget:    ai.Budget{Credits: 1000},
		Prompts:   DefaultPrompts(),
	}
	res, err := r.RunTask(context.Background(), string(ai.TaskCompanyResearch), "domain=acme.com")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if res.Model != "free-model" {
		t.Fatalf("model=%q, want free-model", res.Model)
	}
	var out ai.CompanyResearchOutput
	if err := ai.ValidateInto(res.Raw, &out); err != nil || out.Summary != "Acme makes widgets" {
		t.Fatalf("raw=%q parsed=%+v err=%v", res.Raw, out, err)
	}
}

// TestOrchestrator_EndToEnd_RealSeams wires the orchestrator to the REAL collect + ai seams (Brave
// over httptest, a stub completer) plus a fake Enricher, and asserts a complete Dossier assembles.
func TestOrchestrator_EndToEnd_RealSeams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"web":{"results":[{"title":"Acme Inc","url":"https://acme.com","description":"widgets"}]}}`)
	}))
	defer srv.Close()

	disc := CollectDiscoverer{
		Client: collect.NewClient(egress(provider.StaticKeyResolver{"brave-search:default": "k"})),
		Provider: collect.Provider{Slug: "brave-search", BaseURL: srv.URL, Dialect: collect.DialectBrave,
			Auth: provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-Subscription-Token", KeyPoolSelector: "brave-search:default"}},
	}
	air := CascadeAIRunner{
		Completer: stubCompleter{text: `{"summary":"Acme makes widgets","search_keywords":["widgets"]}`},
		Models:    []ai.Model{{Slug: "free", ModelID: "free-model", Free: true}},
		Budget:    ai.Budget{Credits: 1000}, Prompts: DefaultPrompts(),
	}
	enr := fakeEnricher{vals: map[domain.Field]FieldValue{
		domain.FieldCompanyName: {Value: "Acme", Confidence: 0.8, Provider: "brandfetch", Cost: 3},
	}}

	o := NewOrchestrator(enr, disc, air)
	o.now = fixedClock()
	dossier, err := o.Assemble(context.Background(), Subject{Domain: "acme.com"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if dossier.CompanyProfile["name"] != "Acme" || dossier.AISummary != "Acme makes widgets" {
		t.Fatalf("dossier profile=%v summary=%q", dossier.CompanyProfile, dossier.AISummary)
	}
	if !contains(dossier.SearchKeywords, "Acme Inc") || !contains(dossier.SearchKeywords, "widgets") {
		t.Fatalf("search_keywords=%v", dossier.SearchKeywords)
	}
	if len(dossier.Provenance) < 2 {
		t.Fatalf("expected provenance from enrichment + AI, got %+v", dossier.Provenance)
	}
}
