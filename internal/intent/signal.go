// Package intent is the computed Intent Detection Engine (ADR-0027): it turns collected Intent
// Signals into per-class scores by a DETERMINISTIC, auditable pipeline — decay → weight → corroborate
// → (calibrate) — with per-signal reasoning (explainability, G5). It is a SEPARATE async lane from
// per-Field enrichment, keyed on the Company/account (company_domain). LLM-proposed signals enter
// here only as PROPOSED raw signals (source_type=ai_inference); the customer-visible class score is
// deterministic and reproducible against a pinned Weights config.
//
// Deviation (ADR-0003; ADR-0027 named "log-odds fuse"): engine.fuseAgreeing/logit are unexported, so
// this package implements its own combiner. v1 uses a NOISY-OR corroboration combiner (0 with no
// evidence, monotonic in evidence, bounded [0,1]) with an optional isotonic Calibrator hook
// (calibrate.FitIsotonic is the offline-learned backfill, ADR-0005). Naive-Bayes log-odds fusion with
// learned per-signal likelihood ratios is the calibrated target once labels exist. Every score is
// UNVERIFIED until backtested (docs/research-intelligence/05).
package intent

import (
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// Class is one of the ten computed intent classes (ADR-0027).
type Class string

const (
	ClassBuying                Class = "buying"
	ClassHiring                Class = "hiring"
	ClassTechReplacement       Class = "tech_replacement"
	ClassAIAdoption            Class = "ai_adoption"
	ClassSecurityInvestment    Class = "security_investment"
	ClassCloudMigration        Class = "cloud_migration"
	ClassDigitalTransformation Class = "digital_transformation"
	ClassCRMReplacement        Class = "crm_replacement"
	ClassOutsourcing           Class = "outsourcing"
	ClassMarketingInvestment   Class = "marketing_investment"
)

// AllClasses lists every intent class in stable order.
func AllClasses() []Class {
	return []Class{
		ClassBuying, ClassHiring, ClassTechReplacement, ClassAIAdoption, ClassSecurityInvestment,
		ClassCloudMigration, ClassDigitalTransformation, ClassCRMReplacement, ClassOutsourcing,
		ClassMarketingInvestment,
	}
}

// Valid reports whether c is a known class.
func (c Class) Valid() bool {
	for _, k := range AllClasses() {
		if k == c {
			return true
		}
	}
	return false
}

// Source Type is the provenance origin of a signal (mirrors research.Source; declared here to avoid
// a cross-package dependency on internal/research). AI-proposed signals are never fused as facts.
const (
	SourceAPI     = "api"
	SourceDataset = "dataset"
	SourceAI      = "ai_inference"
)

// Signal is one normalized intent observation feeding a class score (ADR-0027). Losers are retained
// upstream (the store keeps every signal); this is the in-flight shape the Scorer consumes.
type Signal struct {
	Account    string         // company_domain / account key
	Class      Class          // the intent class this signal feeds
	Type       string         // signal type within the class, e.g. "job_posting", "techno_drop"
	Magnitude  float64        // normalized strength in [0,1]
	ObservedAt time.Time      // when the signal was observed (drives decay)
	Provider   string         // producing provider/agent
	SourceType string         // api | dataset | ai_inference
	Confidence float64        // producer confidence in [0,1]
	Cost       domain.Credits // credits charged to collect it (G4/G5)
}
