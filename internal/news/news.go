// Package news is the (roadmap) news & market monitoring subsystem (ADR-0025; docs/research-intelligence/15).
// It is the single owner of the news_items and market_signals tables (migration 0018). What lands now is
// the domain types + the tenant-isolated persistence + its RLS proof; the news-category collection adapters
// and the intent-lane feed attach later, behind the news-monitoring ADR's approval gate (RM-OI-2) — no
// migration needed then.
//
// Boundary (ADR-0025): a NewsItem carries the discovered item's URL + metadata only, never the article
// body. Body extraction stays banned; a discovered URL may only be resolved via a registered provider API.
package news

import (
	"encoding/json"
	"time"
)

// NewsItem is one discovered news/event index entry about an account (company_domain). Index-only: the
// URL + metadata are stored, never the article body.
type NewsItem struct {
	Account     string    // company_domain the item is about
	Source      string    // news-provider slug (e.g. "gdelt")
	Title       string    //
	URL         string    // the item URL (index-only; body never stored)
	Topic       string    // coarse categorization
	PublishedAt time.Time // item publication time (zero => unknown, stored NULL)
}

// MarketSignal is one observed market/financial signal about an account.
type MarketSignal struct {
	Account    string          //
	SignalType string          // e.g. "funding", "stock_move", "hiring_surge"
	Magnitude  float64         //
	Detail     json.RawMessage // raw signal shape (defaults to {})
	ObservedAt time.Time       // when observed (zero => now() at insert)
}
