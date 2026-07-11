package intent

import (
	"context"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// FieldReader reads the current best canonical Field values for a subject — the engine store's read
// model (store.FieldVersions / pgstore.Store satisfy it via Current). The EngineSignalCollector uses
// it to derive intent Signals from data the enrichment engine already collected, so intent_refresh
// works without a separate signal-provider integration (that richer collector is a follow-on).
type FieldReader interface {
	Current(ctx context.Context, subjectID string) (map[domain.Field]domain.FieldValue, error)
}

// EngineSignalCollector is a SignalCollector that derives Intent Signals from the account's current
// enrichment Fields (buying_signal event, funding stage, …). It is a pragmatic v1 bridge: it turns
// already-collected, provider-provenanced data into Signals; a dedicated signal-provider collector
// (job-posting volumes, technographics deltas over time) is the roadmap enrichment.
type EngineSignalCollector struct {
	Fields FieldReader
	now    func() time.Time
}

// NewEngineSignalCollector builds a collector over the engine's Field read model.
func NewEngineSignalCollector(fr FieldReader) *EngineSignalCollector {
	return &EngineSignalCollector{Fields: fr, now: time.Now}
}

func (c *EngineSignalCollector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Collect reads the account's current Fields and maps them to Signals. Provenance (provider,
// confidence) is carried through from the sourced Field, and each Signal's SourceType is `api`.
func (c *EngineSignalCollector) Collect(ctx context.Context, account string) ([]Signal, error) {
	cur, err := c.Fields.Current(ctx, account)
	if err != nil {
		return nil, err
	}
	now := c.clock()
	var signals []Signal

	// buying_signal is an event type ("hiring" | "funding" | "job_change" | …) → a class signal.
	if bs, ok := cur[domain.FieldBuyingSignal]; ok && bs.Value != "" {
		if class, typ := classForBuyingSignal(bs.Value); class != "" {
			signals = append(signals, Signal{
				Account: account, Class: class, Type: typ, Magnitude: 1.0, ObservedAt: now,
				Provider: bs.Prov.Provider, SourceType: SourceAPI, Confidence: float64(bs.Confidence),
			})
		}
	}
	// A known funding stage is a buying-intent signal (weaker than a live surge).
	if fs, ok := cur[domain.FieldFundingStage]; ok && fs.Value != "" {
		signals = append(signals, Signal{
			Account: account, Class: ClassBuying, Type: "funding", Magnitude: 0.8, ObservedAt: now,
			Provider: fs.Prov.Provider, SourceType: SourceAPI, Confidence: float64(fs.Confidence),
		})
	}
	return signals, nil
}

// classForBuyingSignal maps a buying_signal event value to its intent class + signal type. Unknown
// values yield ("",""), i.e. no signal (no fabricated intent).
func classForBuyingSignal(v string) (Class, string) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "hiring":
		return ClassHiring, "job_posting"
	case "funding":
		return ClassBuying, "funding"
	case "job_change", "leadership_change":
		return ClassHiring, "leadership_hiring"
	case "product_launch", "expansion":
		return ClassBuying, "topic_surge"
	default:
		return "", ""
	}
}
