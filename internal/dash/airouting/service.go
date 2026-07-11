// Package airouting is the AI routing / models dashboard surface (Slice 26, docs/research-intelligence/08).
// v1 is a READ-ONLY projection of the internal/ai model cascade registry (ai.Models) — the platform LLM
// catalog the deterministic free→paid cascade orders over (ADR-0026). It owns no tenant tables: the
// registry is platform config, identical for every caller, so this service needs no RLS transaction and
// no store. Editable ai_prompt / llm_route config (config_versions kinds) is a follow-on that first needs
// a kind-CHECK-widen migration (migration 0006 pins the kind vocabulary as a closed CHECK); this catalog
// stands alone until then.
package airouting

import (
	"net/url"

	"github.com/enrichment/waterfall/internal/ai"
)

// ModelInfo is the dashboard projection of one LLM registry entry (ai.Model): the fields an operator
// needs to read the cascade order and per-model cost/inclusion. The egress AuthDescriptor and any
// credential material are intentionally omitted — keys never surface on the wire (ADR-0017).
type ModelInfo struct {
	Slug       string `json:"slug"`
	ModelID    string `json:"model_id"`
	Dialect    string `json:"dialect"`
	Host       string `json:"host"`
	Status     string `json:"status"` // ADR-0009 inclusion verdict
	Free       bool   `json:"free"`
	InPerMTok  int64  `json:"in_per_mtok"`  // UNVERIFIED placeholder cost (sets cascade order + G4 scale)
	OutPerMTok int64  `json:"out_per_mtok"` // UNVERIFIED placeholder cost
	DocsURL    string `json:"docs_url"`
}

// Service projects the static ai.Models registry. No store, no ctx — the catalog is platform config.
type Service struct{}

// NewService constructs the read-only AI models catalog service.
func NewService() *Service { return &Service{} }

// Models returns the LLM registry in cascade order (free-first, exactly as ai.Models declares it),
// projected for the dashboard with keys and raw endpoints stripped.
func (s *Service) Models() []ModelInfo {
	src := ai.Models()
	out := make([]ModelInfo, 0, len(src))
	for _, m := range src {
		out = append(out, ModelInfo{
			Slug:       m.Slug,
			ModelID:    m.ModelID,
			Dialect:    dialectName(m.Dialect),
			Host:       hostOf(m.BaseURL),
			Status:     m.Status,
			Free:       m.Free,
			InPerMTok:  int64(m.InPerMTok),
			OutPerMTok: int64(m.OutPerMTok),
			DocsURL:    m.DocsURL,
		})
	}
	return out
}

// dialectName renders the wire dialect the client speaks to a Model.
func dialectName(d ai.Dialect) string {
	if d == ai.DialectAnthropic {
		return "anthropic"
	}
	return "openai"
}

// hostOf extracts the host of a Model BaseURL (best-effort; empty on a malformed URL).
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}
