package research

import (
	"context"

	"github.com/enrichment/waterfall/internal/ai"
	"github.com/enrichment/waterfall/internal/collect"
)

// This file adapts the real R&I clients to the orchestrator's seams. Each is a thin bridge — the
// orchestrator stays testable against the interfaces, and the concrete clients (which carry the
// egress/breaker/cost machinery) are injected at wiring time. The enrichment seam over engine.Run
// (which needs a Store + Principal) lands with the persistence increment; only Discoverer and
// AIRunner — both PG-free — are wired here.

// CollectDiscoverer adapts a *collect.Client + a chosen search Provider to the Discoverer seam.
type CollectDiscoverer struct {
	Client   *collect.Client
	Provider collect.Provider
}

// Discover runs one bounded search and maps hits to the orchestrator's discovery shape.
func (c CollectDiscoverer) Discover(ctx context.Context, query string, count int) ([]DiscoveryHit, string, error) {
	res, err := c.Client.Search(ctx, c.Provider, collect.Query{Text: query, Count: count})
	if err != nil {
		return nil, c.Provider.Slug, err
	}
	hits := make([]DiscoveryHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		hits = append(hits, DiscoveryHit{Title: h.Title, URL: h.URL, Snippet: h.Snippet})
	}
	return hits, res.Provider, nil
}

// CascadeAIRunner adapts an ai.Completer (e.g. *ai.LLMClient) plus a model set, a per-task budget,
// and a PromptStore to the AIRunner seam. It builds the task prompt (system = Prompt Version, user =
// the orchestrator-supplied input), runs the DETERMINISTIC ai.RunCascade, and returns the accepted
// raw output + accounting. The orchestrator — not the model — chooses the task and input (ADR-0026).
type CascadeAIRunner struct {
	Completer ai.Completer
	Models    []ai.Model
	Budget    ai.Budget
	Prompts   PromptStore
	MaxTokens int
}

// RunTask runs one AI agent task through the free→paid cascade with the task's struct validator.
func (r CascadeAIRunner) RunTask(ctx context.Context, task, input string) (AITaskResult, error) {
	msgs := make([]ai.Message, 0, 2)
	if r.Prompts != nil {
		if sys := r.Prompts.System(task); sys != "" {
			msgs = append(msgs, ai.Message{Role: "system", Content: sys})
		}
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: input})

	maxTok := r.MaxTokens
	if maxTok <= 0 {
		maxTok = 800
	}
	res, err := ai.RunCascade(ctx, r.Completer, ai.CascadeInput{
		Models:   r.Models,
		Request:  ai.CompletionRequest{Messages: msgs, JSON: true, MaxTokens: maxTok, Temperature: 0},
		Validate: validatorFor(task),
		Budget:   r.Budget,
	})
	if err != nil {
		return AITaskResult{}, err
	}
	return AITaskResult{Raw: res.Raw, Model: res.Model, Cost: res.CostCredits}, nil
}

// validatorFor returns the struct-based output validator for a task ("" tasks accept any non-empty
// text). Adding a task = add its typed output + a case here (ADR-0026 struct validation, no schema engine).
func validatorFor(task string) func(raw []byte) error {
	switch task {
	case string(ai.TaskCompanyResearch):
		return func(raw []byte) error { var o ai.CompanyResearchOutput; return ai.ValidateInto(raw, &o) }
	case string(ai.TaskCompetitor):
		return func(raw []byte) error { var o ai.CompetitorListOutput; return ai.ValidateInto(raw, &o) }
	default:
		return nil
	}
}
