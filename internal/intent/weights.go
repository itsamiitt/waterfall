package intent

import "time"

// Weights is the versioned scoring configuration (ADR-0027): a per-(class,type) weight and a
// freshness half-life per signal type. In production this is a `config_versions` kind `intent_weights`
// (the deferred airouting/config surface); a refresh job pins one config version so a re-score is
// reproducible. DefaultWeights() carries the platform cold-start defaults (UNVERIFIED — tuned by the
// offline-learning job once conversion labels exist).
type Weights struct {
	// ClassType[class][type] is the weight applied to a signal of that type within that class.
	ClassType map[Class]map[string]float64
	// HalfLife[type] is the duration over which a signal of that type's magnitude halves.
	HalfLife map[string]time.Duration

	DefaultWeight   float64
	DefaultHalfLife time.Duration
}

// DefaultWeights returns the platform cold-start defaults. Values are UNVERIFIED design placeholders.
func DefaultWeights() Weights {
	day := 24 * time.Hour
	return Weights{
		ClassType: map[Class]map[string]float64{
			ClassBuying:                {"topic_surge": 0.8, "funding": 0.6, "web_visit": 0.5},
			ClassHiring:                {"job_posting": 0.6, "eng_hiring": 0.8, "sales_hiring": 0.5, "leadership_hiring": 0.6},
			ClassTechReplacement:       {"techno_drop": 0.7, "techno_add": 0.5},
			ClassAIAdoption:            {"ai_job_req": 0.7, "ai_tool_add": 0.7, "ai_news": 0.4},
			ClassSecurityInvestment:    {"security_hiring": 0.7, "security_tool_add": 0.6, "breach_news": 0.5},
			ClassCloudMigration:        {"cloud_techno_add": 0.7, "infra_hiring": 0.5},
			ClassDigitalTransformation: {"ai_inference_synthesis": 0.5},
			ClassCRMReplacement:        {"crm_techno_drop": 0.8, "revops_hiring": 0.5},
			ClassOutsourcing:           {"bpo_posting": 0.6, "outsourcing_news": 0.5},
			ClassMarketingInvestment:   {"martech_add": 0.6, "marketing_hiring": 0.5},
		},
		HalfLife: map[string]time.Duration{
			"topic_surge":            7 * day,
			"web_visit":              14 * day,
			"funding":                180 * day,
			"job_posting":            30 * day,
			"eng_hiring":             30 * day,
			"techno_add":             90 * day,
			"techno_drop":            90 * day,
			"ai_news":                21 * day,
			"breach_news":            21 * day,
			"ai_inference_synthesis": 14 * day,
		},
		DefaultWeight:   0.5,
		DefaultHalfLife: 30 * day,
	}
}

// weight returns the configured weight for (class,type), falling back to DefaultWeight.
func (w Weights) weight(class Class, typ string) float64 {
	if m, ok := w.ClassType[class]; ok {
		if v, ok := m[typ]; ok {
			return v
		}
	}
	if w.DefaultWeight > 0 {
		return w.DefaultWeight
	}
	return 0.5
}

// halfLifeHours returns the configured half-life (in hours) for a signal type, falling back to
// DefaultHalfLife then a 30-day default.
func (w Weights) halfLifeHours(typ string) float64 {
	if d, ok := w.HalfLife[typ]; ok && d > 0 {
		return d.Hours()
	}
	if w.DefaultHalfLife > 0 {
		return w.DefaultHalfLife.Hours()
	}
	return (30 * 24 * time.Hour).Hours()
}
