package intent

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// SignalCollector gathers Intent Signals for an account from provider adapters (hiring via
// theirstack/predictleads, technographics deltas via builtwith/wappalyzer, funding via
// firmographics, …) and AI-proposed signals. The refresher depends only on this seam; the real
// collector (mapping provider outputs → Signals) is wired in cmd/enrichapi.
type SignalCollector interface {
	Collect(ctx context.Context, account string) ([]Signal, error)
}

// ScoreWriter persists computed class scores. *Store satisfies it (SaveScores).
type ScoreWriter interface {
	SaveScores(ctx context.Context, account, configVersion string, scores []ClassScore) error
}

// FieldWriter writes a canonical Field value into the enrichment store (field_versions). The engine's
// store.Store / pgstore.Store satisfy it via Append; intent is the SINGLE write-back owner of the
// intent_score / intent_topics / buying_signal Fields (ADR-0027).
type FieldWriter interface {
	Append(ctx context.Context, subjectID string, v domain.FieldValue) error
}

const writebackProvider = "intent-engine"

// IntentRefresher recomputes intent for an account on the async lane (ADR-0027): it collects signals,
// scores them deterministically, persists the per-class scores, and writes back the three canonical
// intent Fields into the waterfall. It is pure w.r.t. its seams (unit-testable); the async job
// submission (job.Kind=intent_refresh) and the real SignalCollector are wired on top.
type IntentRefresher struct {
	Collector      SignalCollector
	Scorer         *Scorer
	Scores         ScoreWriter // optional; persists intent_scores
	Fields         FieldWriter // optional; writes back the canonical Fields
	ConfigVersion  string
	TopicThreshold float64 // min class score to appear in intent_topics
	now            func() time.Time
}

// NewRefresher builds a refresher over a collector + scorer with cold-start defaults.
func NewRefresher(c SignalCollector, s *Scorer) *IntentRefresher {
	return &IntentRefresher{Collector: c, Scorer: s, ConfigVersion: "intent-v1", TopicThreshold: 0.3, now: time.Now}
}

func (r *IntentRefresher) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Refresh runs the async lane for one account and returns the computed class scores.
func (r *IntentRefresher) Refresh(ctx context.Context, account string) ([]ClassScore, error) {
	signals, err := r.Collector.Collect(ctx, account)
	if err != nil {
		return nil, err
	}
	scores := r.Scorer.ScoreAll(signals)
	if r.Scores != nil {
		if err := r.Scores.SaveScores(ctx, account, r.ConfigVersion, scores); err != nil {
			return scores, err
		}
	}
	if r.Fields != nil {
		if err := r.writeBack(ctx, account, scores); err != nil {
			return scores, err
		}
	}
	return scores, nil
}

// writeBack projects the scores onto the three canonical Fields and appends each into field_versions
// with intent provenance (G5). Nothing is written when there is no intent (empty projection).
func (r *IntentRefresher) writeBack(ctx context.Context, account string, scores []ClassScore) error {
	wb := Project(scores, r.TopicThreshold)
	conf := domain.Confidence(clamp01(wb.Confidence))
	now := r.clock()
	for f, val := range wb.Fields() {
		fv := domain.FieldValue{
			Field:      f,
			Value:      val,
			Confidence: conf,
			Prov: domain.Provenance{
				Provider:       writebackProvider,
				ObservedAt:     now,
				Confidence:     conf,
				IdempotencyKey: "intent:" + account + ":" + string(f) + ":" + r.ConfigVersion,
			},
		}
		if !fv.Valid() {
			continue
		}
		if err := r.Fields.Append(ctx, account, fv); err != nil {
			return err
		}
	}
	return nil
}
