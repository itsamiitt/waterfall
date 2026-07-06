package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Subject is the thing being enriched — a Person or a Company (docs/00 §7). It holds
// the known input attributes (e.g. a name + company_domain) that providers match on.
// The SubjectID is a stable, tenant-scoped identifier for the record.
type Subject struct {
	ID    string           // stable record id within the tenant
	Known map[Field]string // input attributes already known (match keys)
}

// EnrichmentRequest is one unit of enrichment work for a single Subject: which Fields
// are wanted, to what Confidence, and under what per-record Cost Ceiling (G4).
type EnrichmentRequest struct {
	JobID            string     // enrichment job this record belongs to (cost is pooled per job)
	Subject          Subject    // the record to enrich
	Want             []Field    // requested Fields
	ConfidenceTarget Confidence // stop filling a Field once fused confidence reaches this
	CostCeiling      Credits    // hard cap on committed provider spend for this record (G4)
	ConfigVersion    string     // routing/config version — part of the idempotency key
	// WorkflowKey/Country are OPTIONAL usage-attribution metadata (T5c/OI-P4-1b): the engine tags
	// the rotation lease context with them so every leased provider call emits a fully-attributed
	// usage row. Observability dimensions only — deliberately NOT part of the G2 idempotency key
	// (IdempotencyKey hashes its enumerated fields, not this struct), so a replay with different
	// attribution still reuses the same stored results.
	WorkflowKey string // published waterfall workflow driving this request ("" = unattributed)
	Country     string // Subject country for per-country usage/cost rollups ("" = unknown)
}

// IdempotencyKey derives the canonical G2 key for a single provider call within this
// request (skills/waterfall-correctness G2; docs/04 §4, docs/09 §2):
//
//	hash(tenant_id, record_id, field, provider, normalized_request_params, config_version)
//
// A deliberate re-fetch is expressed by changing config_version or params, which yields
// a new key; an accidental replay reuses the same key and is short-circuited.
func (r EnrichmentRequest) IdempotencyKey(tenantID string, field Field, provider string) string {
	h := sha256.New()
	// Length-prefixed writes so field boundaries are unambiguous (avoids the
	// "ab"+"c" == "a"+"bc" collision class).
	for _, part := range []string{
		tenantID,
		r.Subject.ID,
		string(field),
		provider,
		normalizeParams(r.Subject.Known),
		r.ConfigVersion,
	} {
		writeLenPrefixed(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeParams renders the known-attribute map into a stable, canonical string so
// the same inputs always hash identically regardless of map iteration order.
func normalizeParams(known map[Field]string) string {
	if len(known) == 0 {
		return ""
	}
	keys := make([]string, 0, len(known))
	for k := range known {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strings.ToLower(strings.TrimSpace(known[Field(k)])))
		b.WriteByte('\x1f') // unit separator
	}
	return b.String()
}

func writeLenPrefixed(h interface{ Write([]byte) (int, error) }, s string) {
	var l [8]byte
	n := uint64(len(s))
	for i := 0; i < 8; i++ {
		l[i] = byte(n >> (8 * i))
	}
	_, _ = h.Write(l[:])
	_, _ = h.Write([]byte(s))
}
