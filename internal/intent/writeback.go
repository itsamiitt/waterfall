package intent

import (
	"sort"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
)

// Writeback is the canonical-Field projection of a set of ClassScores — the single normalized values
// the intent engine writes back into the waterfall (field_versions) as intent_score / intent_topics /
// buying_signal (ADR-0027). intent is the ONLY writer of these three Fields; the richer per-class
// breakdown stays in intent_scores and is exposed via the intent API, never overloaded onto the
// single-valued Fields.
type Writeback struct {
	IntentScore  string  // the strongest class's score, formatted in [0,1]
	IntentTopics string  // classes at/above the topic threshold, score-desc, comma-joined (normalized list)
	BuyingSignal string  // the single strongest class (the dominant signal)
	Confidence   float64 // the strongest class's confidence
	TopClass     Class   // the strongest class
}

// Project maps class scores onto the canonical Field projection. Classes scoring below minTopic are
// omitted from intent_topics; an empty score set yields a zero Writeback. The projection is
// deterministic (scores sorted desc, ties broken by class name).
func Project(scores []ClassScore, minTopic float64) Writeback {
	if len(scores) == 0 {
		return Writeback{}
	}
	sorted := make([]ClassScore, len(scores))
	copy(sorted, scores)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].Class < sorted[j].Class
	})
	top := sorted[0]
	topics := make([]string, 0, len(sorted))
	for _, cs := range sorted {
		if cs.Score >= minTopic {
			topics = append(topics, string(cs.Class))
		}
	}
	return Writeback{
		IntentScore:  strconv.FormatFloat(top.Score, 'f', 3, 64),
		IntentTopics: strings.Join(topics, ","),
		BuyingSignal: string(top.Class),
		Confidence:   top.Confidence,
		TopClass:     top.Class,
	}
}

// Fields returns the write-back as canonical (Field, value) pairs for the field_versions store. Only
// the three intent Fields are ever produced; empty components are omitted.
func (w Writeback) Fields() map[domain.Field]string {
	m := map[domain.Field]string{}
	if w.IntentScore != "" {
		m[domain.FieldIntentScore] = w.IntentScore
	}
	if w.IntentTopics != "" {
		m[domain.FieldIntentTopics] = w.IntentTopics
	}
	if w.BuyingSignal != "" {
		m[domain.FieldBuyingSignal] = w.BuyingSignal
	}
	return m
}
