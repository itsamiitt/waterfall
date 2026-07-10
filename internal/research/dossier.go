// Package research is the R&I orchestration layer (ADR-0028): it assembles a normalized, CRM-ready
// Dossier for a subject (domain → dossier) by composing the collection (internal/collect), AI
// (internal/ai), and enrichment (engine) seams in a DETERMINISTIC order. The orchestrator — never a
// model — chooses which steps run (ADR-0026). This file defines the Dossier response schema (mirrors
// docs/research-intelligence/06 + openapi-research.json); orchestrator.go holds the assembly DAG.
//
// Multi-valued / relational data (competitors, news, …) lives ONLY in the Dossier, never as a
// canonical Field (ADR-0028 boundary). Every value carries provenance with a Source Type
// (api | dataset | ai_inference); AI-inferred values are kept distinct and never fused as facts.
package research

import (
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// Source Type is the provenance origin of a Dossier value (ADR-0026/0028).
const (
	SourceAPI     = "api"
	SourceDataset = "dataset"
	SourceAI      = "ai_inference"
)

// Source is one provenance row (G5): which provider produced a value, of what type, at what cost.
type Source struct {
	Field      string         `json:"field"`
	Provider   string         `json:"provider"`
	SourceType string         `json:"source_type"`
	Cost       domain.Credits `json:"cost"`
	IdemKey    string         `json:"idem_key,omitempty"`
	Confidence float64        `json:"confidence"`
}

// Competitor is one competitor entry (Dossier-only; ADR-0028).
type Competitor struct {
	Name   string `json:"name"`
	Domain string `json:"domain,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// NewsItem is one news reference (Dossier-only).
type NewsItem struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Date  string `json:"date,omitempty"`
}

// IntentSection is the account intent summary carried in the Dossier. Intent is computed on a
// separate async lane (ADR-0027); a synchronous assembly shows the last-known scores or Status
// "pending", never a blocking compute.
type IntentSection struct {
	Score         string   `json:"intent_score,omitempty"`
	Topics        []string `json:"intent_topics,omitempty"`
	BuyingSignals []string `json:"buying_signals,omitempty"`
	Status        string   `json:"status,omitempty"` // "" | "pending"
}

// Subject is the resolved research target.
type Subject struct {
	Domain      string            `json:"domain,omitempty"`
	Name        string            `json:"name,omitempty"`
	ResolvedIDs map[string]string `json:"resolved_ids,omitempty"`
}

// CRMReady is the normalized projection a CRM connector (ADR-0030) can ingest without transformation.
type CRMReady struct {
	Account map[string]string `json:"account"`
	Contact map[string]string `json:"contact,omitempty"`
}

// ConfidenceSection carries overall + per-section calibrated confidence.
type ConfidenceSection struct {
	Overall   float64            `json:"overall"`
	BySection map[string]float64 `json:"by_section,omitempty"`
}

// Metadata carries the config version the Dossier was assembled under.
type Metadata struct {
	ConfigVersion int `json:"config_version"`
}

// DataFreshness records when the Dossier was generated and per-section last-updated stamps.
type DataFreshness struct {
	GeneratedAt time.Time            `json:"generated_at"`
	LastUpdated map[string]time.Time `json:"last_updated,omitempty"`
}

// Dossier is the normalized, CRM-ready intelligence document (ADR-0028). Single-valued firmographic
// data references canonical Field values; everything multi-valued/relational is Dossier-only.
type Dossier struct {
	DossierID      string            `json:"dossier_id"`
	Subject        Subject           `json:"subject"`
	CompanyProfile map[string]string `json:"company_profile"`
	ContactProfile map[string]string `json:"contact_profile,omitempty"`
	Firmographics  map[string]string `json:"firmographics"`
	Technographics []string          `json:"technographics,omitempty"`
	HiringSignals  []string          `json:"hiring_signals,omitempty"`
	Intent         IntentSection     `json:"intent"`
	News           []NewsItem        `json:"news,omitempty"`
	Competitors    []Competitor      `json:"competitors,omitempty"`
	AISummary      string            `json:"ai_summary,omitempty"`
	AIReasoning    string            `json:"ai_reasoning,omitempty"`
	SearchKeywords []string          `json:"search_keywords,omitempty"`
	CRMReady       CRMReady          `json:"crm_ready"`
	Confidence     ConfidenceSection `json:"confidence"`
	Provenance     []Source          `json:"provenance"`
	ProcessingLog  []string          `json:"processing_log"`
	Metadata       Metadata          `json:"metadata"`
	DataFreshness  DataFreshness     `json:"data_freshness"`
}
