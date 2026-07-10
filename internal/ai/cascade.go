package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
)

// Budget bounds cascade spend (G4). It shrinks as models are charged on their actual token usage.
type Budget struct {
	Credits domain.Credits
}

// CascadeInput is one deterministic free→paid cascade.
type CascadeInput struct {
	Models      []Model                // candidate models; ordered free-first by Order()
	Request     CompletionRequest      // the same prompt for every attempt
	Validate    func(raw []byte) error // struct-based output check; nil ⇒ accept any non-empty text
	Budget      Budget                 // G4 spend ceiling for the whole cascade
	MaxAttempts int                    // cap across the cascade; ≤0 ⇒ len(models)
}

// RejectedAttempt is a losing attempt, retained for provenance (G5). Reason is a DETERMINISTIC
// signal — "schema_invalid", "budget", or "call_error:<CLASS>" — NEVER a model's self-confidence.
type RejectedAttempt struct {
	Model  string
	Reason string
	Usage  Usage
}

// CascadeResult is the accepted outcome plus provenance (G5).
type CascadeResult struct {
	Raw         []byte
	Model       string            // ModelID that produced the accepted answer
	Usage       Usage             // the accepted call's token usage
	CostCredits domain.Credits    // TOTAL credits charged across the whole cascade (G4)
	Attempts    int               // models actually called (budget-skipped models don't count)
	Escalations int               // escalations past the first attempt
	Rejected    []RejectedAttempt // losers retained (G5)
}

// ErrCascadeExhausted means no model produced an accepted answer within budget/attempts.
var ErrCascadeExhausted = errors.New("model cascade exhausted with no valid completion")

// Order returns models sorted free-first (stable within each partition) — the ADR-0007 cheap-first
// cascade applied to models. It does not mutate the input.
func Order(models []Model) []Model {
	out := make([]Model, len(models))
	copy(out, models)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Free && !out[j].Free })
	return out
}

// estInputTokens is a rough pre-call token estimate (≈4 chars/token) for the G4 reserve-before-call
// budget gate; the actual token count from the completion is charged after.
func estInputTokens(req CompletionRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	return n/4 + 1
}

// estCost is the pre-call reservation estimate for a model (G4 reserve-before-call).
func estCost(m Model, req CompletionRequest) domain.Credits {
	out := req.MaxTokens
	if out <= 0 {
		out = 1024
	}
	return m.costOf(Usage{InputTokens: estInputTokens(req), OutputTokens: out})
}

// RunCascade tries models free→paid until one returns an accepted completion within budget.
//
// The dispose gate is fully DETERMINISTIC (ADR-0026): a completion is ACCEPTED iff the call
// succeeded AND its output passes the (struct-based) Validate AND the model was within the G4
// budget AND the attempt cap was not exceeded. Escalation is triggered ONLY by these deterministic
// signals — never by a model's self-reported confidence, and the model never selects the next model
// (the order is fixed here by Order()). Every losing attempt is retained (G5).
func RunCascade(ctx context.Context, cp Completer, in CascadeInput) (CascadeResult, error) {
	models := Order(in.Models)
	maxAtt := in.MaxAttempts
	if maxAtt <= 0 || maxAtt > len(models) {
		maxAtt = len(models)
	}
	remaining := in.Budget.Credits
	var spent domain.Credits
	res := CascadeResult{}

	for _, m := range models {
		if res.Attempts >= maxAtt {
			break
		}
		// G4 reserve-before-call: skip a model whose estimated spend exceeds the remaining budget.
		if est := estCost(m, in.Request); est > remaining {
			res.Rejected = append(res.Rejected, RejectedAttempt{Model: m.ModelID, Reason: "budget"})
			continue
		}
		if res.Attempts > 0 {
			res.Escalations++
		}
		res.Attempts++

		comp, err := cp.Complete(ctx, m, in.Request)
		if err != nil {
			res.Rejected = append(res.Rejected, RejectedAttempt{
				Model:  m.ModelID,
				Reason: "call_error:" + domain.ClassOf(err).String(),
			})
			if ctx.Err() != nil { // parent cancelled/deadline — stop the whole cascade
				res.CostCredits = spent
				return res, ctx.Err()
			}
			continue // deterministic escalation on a call error
		}

		// G4 charge-on-actual (the tokens were spent whether or not we accept the answer).
		cost := m.costOf(comp.Usage)
		spent += cost
		remaining -= cost

		accepted := true
		if in.Validate != nil {
			if verr := in.Validate([]byte(comp.Text)); verr != nil {
				accepted = false
			}
		} else if strings.TrimSpace(comp.Text) == "" {
			accepted = false
		}
		if !accepted {
			res.Rejected = append(res.Rejected, RejectedAttempt{
				Model: m.ModelID, Reason: "schema_invalid", Usage: comp.Usage,
			})
			continue // deterministic escalation on schema failure
		}

		// Dispose = ACCEPT.
		res.Raw = []byte(comp.Text)
		res.Model = comp.Model
		res.Usage = comp.Usage
		res.CostCredits = spent
		return res, nil
	}
	res.CostCredits = spent
	return res, fmt.Errorf("%w (%d attempts)", ErrCascadeExhausted, res.Attempts)
}
