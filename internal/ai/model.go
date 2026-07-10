// Package ai is the AI Research layer (ADR-0026): LLM inference as a bounded, cost-metered egress
// call, plus the deterministic free→paid Model Cascade and struct-based output validation.
//
// LLM-as-egress-adapter, with a recorded deviation (docs/research-intelligence/04-ai-pipeline.md
// D-1): the reusable egress machinery — key injection at the boundary via provider.AuthInjector,
// the SSRF choke, the circuit Breaker, the bounded CallPolicy, and oauth2-cc — is reused verbatim,
// but an LLM call is chat-messages-in / free-form-JSON-out, which does not fit the Field-shaped
// provider.Adapter contract. So the AI layer uses a dedicated LLMClient (this package) that
// attaches a provider.AuthDescriptor via provider.WithAuthDescriptor and never holds a key. LLM
// Models are a SEPARATE registry (Models()), NOT the enrichment adapters registry — an LLM fills
// no Fields and must never be wired into the enrichment engine.
//
// Governing invariant (ADR-0026): the model PROPOSES; a deterministic gate DISPOSES every spend,
// tool, and escalation. The cascade never escalates on a model's self-reported confidence, and the
// model never chooses a tool — RunCascade fixes the model order and the dispose gate reads only
// deterministic signals (schema-valid, budget, attempt count).
package ai

import (
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Dialect is the request/response wire shape of an LLM gateway.
type Dialect int

const (
	// DialectOpenAI is the OpenAI-compatible /chat/completions schema (OpenRouter, OpenAI, and
	// most gateways): {model, messages:[{role,content}], …} → {choices:[{message:{content}}],
	// usage:{prompt_tokens, completion_tokens}}.
	DialectOpenAI Dialect = iota
	// DialectAnthropic is the Anthropic Messages schema: x-api-key + anthropic-version headers,
	// a top-level "system" field, {model, max_tokens, messages} → {content:[{type,text}],
	// usage:{input_tokens, output_tokens}}.
	DialectAnthropic
)

// Model is one LLM the cascade can call — the AI-layer analogue of a provider catalog row: a
// stable slug (also the key-pool selector prefix), the egress endpoint + AuthDescriptor (so the
// AuthInjector leases and injects the key), the wire Dialect, a Free flag for free-first cascade
// ordering, and per-1M-token costs for G4 accounting. It holds NO secret.
type Model struct {
	Slug       string                  // stable id + key-pool selector prefix, e.g. "openrouter"
	ModelID    string                  // the gateway's model identifier
	BaseURL    string                  // chat/completions (or messages) endpoint
	Dialect    Dialect                 //
	Auth       provider.AuthDescriptor // scheme + key-pool selector; egress injects the key
	Status     string                  // ADR-0009 inclusion verdict
	Free       bool                    // part of a no-cost pool (free-first ordering)
	InPerMTok  domain.Credits          // input cost per 1M tokens (0 for free) — UNVERIFIED placeholder
	OutPerMTok domain.Credits          // output cost per 1M tokens — UNVERIFIED placeholder
	DocsURL    string                  //
}

// bearer builds a Bearer AuthDescriptor for a key pool (OpenRouter/OpenAI).
func bearer(sel string) provider.AuthDescriptor {
	return provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: sel}
}

// apiKeyHeader builds an api-key-header AuthDescriptor (Anthropic's x-api-key).
func apiKeyHeader(sel, header string) provider.AuthDescriptor {
	return provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: header, KeyPoolSelector: sel}
}

// Models is the append-only LLM registry the cascade orders over. openrouter (free pool) is the
// primary; the paid models are gated escalation targets. Per-token costs are UNVERIFIED design
// placeholders (docs/research-intelligence/11) — they only set the cascade's free→paid ordering
// and the G4 accounting scale, not a billed price. This is NOT the enrichment adapter registry.
func Models() []Model {
	return []Model{
		{
			Slug: "openrouter", ModelID: "meta-llama/llama-3.3-70b-instruct:free",
			BaseURL: "https://openrouter.ai/api/v1/chat/completions", Dialect: DialectOpenAI,
			Auth: bearer("openrouter:default"), Status: "ACTIVE-CANDIDATE", Free: true,
			DocsURL: "https://openrouter.ai/docs",
		},
		{
			Slug: "openrouter-paid", ModelID: "openai/gpt-4o-mini",
			BaseURL: "https://openrouter.ai/api/v1/chat/completions", Dialect: DialectOpenAI,
			Auth: bearer("openrouter:default"), Status: "ACTIVE-CANDIDATE", Free: false,
			InPerMTok: 150, OutPerMTok: 600, DocsURL: "https://openrouter.ai/docs",
		},
		{
			Slug: "openai", ModelID: "gpt-4o-mini",
			BaseURL: "https://api.openai.com/v1/chat/completions", Dialect: DialectOpenAI,
			Auth: bearer("openai:default"), Status: "ACTIVE-CANDIDATE", Free: false,
			InPerMTok: 150, OutPerMTok: 600, DocsURL: "https://platform.openai.com/docs/api-reference/chat",
		},
		{
			Slug: "anthropic", ModelID: "claude-haiku-4-5",
			BaseURL: "https://api.anthropic.com/v1/messages", Dialect: DialectAnthropic,
			Auth: apiKeyHeader("anthropic:default", "x-api-key"), Status: "ACTIVE-CANDIDATE", Free: false,
			InPerMTok: 100, OutPerMTok: 500, DocsURL: "https://docs.anthropic.com/en/api/messages",
		},
	}
}

// Hosts returns the distinct hostnames of every Model's BaseURL, for extending the egress SSRF
// allow-list (provider.NewHostAllowList) — LLM calls traverse the same egress client as providers.
func Hosts() []string {
	seen := map[string]struct{}{}
	var hosts []string
	for _, m := range Models() {
		u, err := url.Parse(m.BaseURL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if _, ok := seen[u.Hostname()]; ok {
			continue
		}
		seen[u.Hostname()] = struct{}{}
		hosts = append(hosts, u.Hostname())
	}
	return hosts
}

// costOf computes the G4 credit cost of a completion from its token usage and the model's
// per-1M-token rates (integer credits; free models cost 0).
func (m Model) costOf(u Usage) domain.Credits {
	return (domain.Credits(u.InputTokens)*m.InPerMTok + domain.Credits(u.OutputTokens)*m.OutPerMTok) / 1_000_000
}
