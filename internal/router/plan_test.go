package router_test

import (
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
)

func TestPlan_OrdersByReservationValue(t *testing.T) {
	// Same field, three providers with different confidence-per-credit densities.
	p1 := providertest.New("expensive", "v", 0.90, 10, domain.FieldWorkEmail) // density .090
	p2 := providertest.New("cheap", "v", 0.80, 2, domain.FieldWorkEmail)      // density .400
	p3 := providertest.New("mid", "v", 0.95, 5, domain.FieldWorkEmail)        // density .190

	pl := router.New(p1, p2, p3).Plan(domain.EnrichmentRequest{
		Want: []domain.Field{domain.FieldWorkEmail},
	})

	steps := pl.ByField[domain.FieldWorkEmail]
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(steps))
	}
	wantOrder := []string{"cheap", "mid", "expensive"}
	for i, w := range wantOrder {
		if steps[i].Provider != w {
			t.Fatalf("step %d: want %s, got %s (order=%v)", i, w, steps[i].Provider, providerNames(steps))
		}
	}
}

func TestPlan_IsDeterministic(t *testing.T) {
	p1 := providertest.New("a", "v", 0.9, 3, domain.FieldWorkEmail)
	p2 := providertest.New("b", "v", 0.9, 3, domain.FieldWorkEmail) // identical density+cost => name tiebreak
	req := domain.EnrichmentRequest{Want: []domain.Field{domain.FieldWorkEmail}}

	first := providerNames(router.New(p1, p2).Plan(req).ByField[domain.FieldWorkEmail])
	second := providerNames(router.New(p2, p1).Plan(req).ByField[domain.FieldWorkEmail])
	if first[0] != "a" || second[0] != "a" {
		t.Fatalf("tie should break deterministically by name: %v vs %v", first, second)
	}
}

func TestPlan_SkipsNonCanonicalFields(t *testing.T) {
	p := providertest.New("a", "v", 0.9, 1, domain.FieldWorkEmail)
	pl := router.New(p).Plan(domain.EnrichmentRequest{Want: []domain.Field{"garbage_field", domain.FieldWorkEmail}})
	if _, ok := pl.ByField["garbage_field"]; ok {
		t.Fatal("planner must not plan calls for a non-canonical field")
	}
	if len(pl.Order) != 1 || pl.Order[0] != domain.FieldWorkEmail {
		t.Fatalf("unexpected plan order: %v", pl.Order)
	}
}

// fixedScorer ranks providers by a fixed table, ignoring the static prior — enough to
// prove the scorer (a bandit stand-in) reorders the cascade.
type fixedScorer map[string]float64

func (f fixedScorer) Score(provider string, _ domain.Field, _ domain.Confidence) float64 {
	return f[provider]
}

func TestPlan_ScorerReordersCascade(t *testing.T) {
	// Static ordering would put "a" first (name tiebreak at equal density); the scorer
	// flips it by scoring "b" higher.
	a := providertest.New("a", "v", 0.9, 1, domain.FieldWorkEmail)
	b := providertest.New("b", "v", 0.9, 1, domain.FieldWorkEmail)
	req := domain.EnrichmentRequest{Want: []domain.Field{domain.FieldWorkEmail}}

	plan := router.New(a, b).WithScorer(fixedScorer{"a": 0.1, "b": 0.9}).Plan(req)
	steps := plan.ByField[domain.FieldWorkEmail]
	if steps[0].Provider != "b" {
		t.Fatalf("scorer should order b first, got %v", providerNames(steps))
	}
}

func providerNames(steps []router.Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Provider
	}
	return out
}
