package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
)

type fakeResp struct {
	comp Completion
	err  error
}

// fakeCompleter substitutes the LLM transport so the cascade's deterministic dispose logic is
// exercised without any HTTP. It records the ModelIDs it was asked to Complete, in order.
type fakeCompleter struct {
	responses map[string]fakeResp
	calls     []string
}

func (f *fakeCompleter) Complete(_ context.Context, m Model, _ CompletionRequest) (Completion, error) {
	f.calls = append(f.calls, m.ModelID)
	r, ok := f.responses[m.ModelID]
	if !ok {
		return Completion{}, domain.NewProviderError(m.Slug, domain.ClassProviderDown, errors.New("no fake response"))
	}
	if r.err != nil {
		return Completion{}, r.err
	}
	c := r.comp
	c.Model = m.ModelID
	return c, nil
}

func validateCompany(raw []byte) error {
	var o CompanyResearchOutput
	return ValidateInto(raw, &o)
}

// TestCascade_FreeFirstAcceptStopsEarly proves the cascade tries the FREE model first (even when
// listed last) and accepts on the first schema-valid completion — never calling the paid model.
func TestCascade_FreeFirstAcceptStopsEarly(t *testing.T) {
	f := &fakeCompleter{responses: map[string]fakeResp{
		"free-model": {comp: Completion{Text: `{"summary":"acme makes widgets"}`, Usage: Usage{InputTokens: 10, OutputTokens: 20}}},
		"paid-model": {comp: Completion{Text: `{"summary":"should never be used"}`}},
	}}
	res, err := RunCascade(context.Background(), f, CascadeInput{
		Models:   []Model{{Slug: "paid", ModelID: "paid-model"}, {Slug: "free", ModelID: "free-model", Free: true}},
		Request:  CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 100},
		Validate: validateCompany,
		Budget:   Budget{Credits: 1000},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "free-model" {
		t.Fatalf("accepted model = %q, want free-model", res.Model)
	}
	if res.Attempts != 1 || res.Escalations != 0 {
		t.Fatalf("attempts=%d escalations=%d, want 1/0", res.Attempts, res.Escalations)
	}
	if len(f.calls) != 1 || f.calls[0] != "free-model" {
		t.Fatalf("calls=%v, want [free-model] (paid model must not be called)", f.calls)
	}
}

// TestCascade_EscalatesOnSchemaInvalid proves the ONLY escalation trigger from a successful call is
// a deterministic schema failure — and the loser is retained (G5).
func TestCascade_EscalatesOnSchemaInvalid(t *testing.T) {
	f := &fakeCompleter{responses: map[string]fakeResp{
		"free-model": {comp: Completion{Text: `this is not json at all`, Usage: Usage{InputTokens: 5, OutputTokens: 5}}},
		"paid-model": {comp: Completion{Text: `{"summary":"validated answer"}`, Usage: Usage{InputTokens: 6, OutputTokens: 8}}},
	}}
	res, err := RunCascade(context.Background(), f, CascadeInput{
		Models:   []Model{{Slug: "free", ModelID: "free-model", Free: true}, {Slug: "paid", ModelID: "paid-model", OutPerMTok: 1000}},
		Request:  CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 100},
		Validate: validateCompany,
		Budget:   Budget{Credits: 1_000_000},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "paid-model" || res.Attempts != 2 || res.Escalations != 1 {
		t.Fatalf("got model=%q attempts=%d escalations=%d, want paid-model/2/1", res.Model, res.Attempts, res.Escalations)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Reason != "schema_invalid" || res.Rejected[0].Model != "free-model" {
		t.Fatalf("rejected=%+v, want one schema_invalid for free-model", res.Rejected)
	}
}

// TestCascade_IgnoresSelfReportedConfidence proves a schema-valid answer is ACCEPTED even when it
// carries a low self-reported confidence field — the dispose gate reads schema validity, not the
// model's opinion of itself (ADR-0026: never escalate on self-confidence).
func TestCascade_IgnoresSelfReportedConfidence(t *testing.T) {
	f := &fakeCompleter{responses: map[string]fakeResp{
		"free-model": {comp: Completion{Text: `{"summary":"ok","confidence":0.001,"self_rating":"very low"}`}},
		"paid-model": {comp: Completion{Text: `{"summary":"unused"}`}},
	}}
	res, err := RunCascade(context.Background(), f, CascadeInput{
		Models:   []Model{{Slug: "free", ModelID: "free-model", Free: true}, {Slug: "paid", ModelID: "paid-model"}},
		Request:  CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}},
		Validate: validateCompany,
		Budget:   Budget{Credits: 1000},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "free-model" || res.Attempts != 1 {
		t.Fatalf("got model=%q attempts=%d, want free-model/1 (self-confidence must not force escalation)", res.Model, res.Attempts)
	}
}

// TestCascade_BudgetSkipsPaid proves the G4 reserve-before-call gate skips a model whose estimated
// spend exceeds the remaining budget — deterministically, not via any model signal.
func TestCascade_BudgetSkipsPaid(t *testing.T) {
	f := &fakeCompleter{responses: map[string]fakeResp{
		"free-model": {comp: Completion{Text: `not valid json`}},
		"paid-model": {comp: Completion{Text: `{"summary":"would be valid but unaffordable"}`}},
	}}
	res, err := RunCascade(context.Background(), f, CascadeInput{
		// paid costs 1 credit/token; MaxTokens 1000 ⇒ est ≈ 1000 credits, over the budget of 10.
		Models:   []Model{{Slug: "free", ModelID: "free-model", Free: true}, {Slug: "paid", ModelID: "paid-model", OutPerMTok: 1_000_000}},
		Request:  CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 1000},
		Validate: validateCompany,
		Budget:   Budget{Credits: 10},
	})
	if !errors.Is(err, ErrCascadeExhausted) {
		t.Fatalf("err=%v, want ErrCascadeExhausted", err)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (paid model must be budget-skipped, not called)", res.Attempts)
	}
	if len(f.calls) != 1 || f.calls[0] != "free-model" {
		t.Fatalf("calls=%v, want [free-model]", f.calls)
	}
	var sawBudget, sawSchema bool
	for _, r := range res.Rejected {
		switch r.Reason {
		case "budget":
			sawBudget = true
		case "schema_invalid":
			sawSchema = true
		}
	}
	if !sawBudget || !sawSchema {
		t.Fatalf("rejected=%+v, want both a budget skip and a schema_invalid", res.Rejected)
	}
}

// TestCascade_EscalatesOnCallErrorAndCharges proves a call error escalates deterministically
// ("call_error:<CLASS>" loser retained) and that accepted-path token cost is charged (G4).
func TestCascade_EscalatesOnCallErrorAndCharges(t *testing.T) {
	f := &fakeCompleter{responses: map[string]fakeResp{
		"free-model": {err: domain.NewProviderError("free", domain.ClassAuth, errors.New("bad key"))},
		"paid-model": {comp: Completion{Text: `{"summary":"ok"}`, Usage: Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}}},
	}}
	res, err := RunCascade(context.Background(), f, CascadeInput{
		Models:   []Model{{Slug: "free", ModelID: "free-model", Free: true}, {Slug: "paid", ModelID: "paid-model", InPerMTok: 100, OutPerMTok: 500}},
		Request:  CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}, MaxTokens: 10},
		Validate: validateCompany,
		Budget:   Budget{Credits: 1000},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Model != "paid-model" || res.Attempts != 2 {
		t.Fatalf("got model=%q attempts=%d, want paid-model/2", res.Model, res.Attempts)
	}
	// 1M in-tokens * 100/MTok + 1M out * 500/MTok = 100 + 500 = 600 credits.
	if res.CostCredits != 600 {
		t.Fatalf("CostCredits=%d, want 600 (G4 charge-on-actual)", res.CostCredits)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Reason != "call_error:AUTH" {
		t.Fatalf("rejected=%+v, want one call_error:AUTH", res.Rejected)
	}
}
