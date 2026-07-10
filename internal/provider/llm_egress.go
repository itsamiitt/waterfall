package provider

import (
	"context"

	"github.com/enrichment/waterfall/internal/domain"
)

// This file exports the egress seam for callers OUTSIDE package provider — specifically the AI
// research layer's LLM client (internal/ai, ADR-0026). An LLM call is chat-messages-in /
// free-form-JSON-out, which does NOT fit the Field-shaped Adapter contract
// (Request{Known,Fields} -> Result{Values map[Field]Observation}); routing it through
// HTTPAdapter would abuse the Field model. Instead the AI layer builds its own request and
// reuses the SAME egress machinery — key injection at the boundary, the SSRF choke, oauth2-cc
// exchange, the breaker, and the bounded-call budget — by attaching an AuthDescriptor with
// WithAuthDescriptor and classifying the response with ClassifyStatus.
//
// Secret containment is unchanged (ADR-0010): the AuthInjector RoundTripper on the egress
// *http.Client still holds and injects the leased key; the AI caller never sees a secret. This
// deviation from the "reuse HTTPAdapter verbatim" wording is recorded in
// docs/research-intelligence/04-ai-pipeline.md (D-1).

// WithAuthDescriptor attaches d to ctx so the AuthInjector injects the leased credential
// (Bearer / api-key-header / oauth2-cc / …) on the outbound request. It is the exported form of
// the internal withAuthDescriptor used by HTTPAdapter, for authenticated egress callers that do
// not use the HTTPAdapter Fetch path. It is a no-op at the injector when d.KeyPoolSelector == "".
func WithAuthDescriptor(ctx context.Context, d AuthDescriptor) context.Context {
	return withAuthDescriptor(ctx, d)
}

// ClassifyStatus maps an HTTP status code onto the normalized ErrorClass taxonomy; ok=true means
// a 2xx the caller should decode. Non-HTTPAdapter egress callers reuse the same mapping the
// HTTPAdapter uses, so LLM/search/dataset failures classify identically (AUTH/QUOTA/RATE_LIMIT/…)
// and feed the breaker and rotation trigger state machine the same way.
func ClassifyStatus(code int) (domain.ErrorClass, bool) {
	return classifyStatus(code)
}
