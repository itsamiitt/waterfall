package domain

import "time"

// Provenance records which Provider produced a Field value, when, at what cost, with
// what confidence, and under which idempotency key (docs/00 §7; gate G5 in
// skills/waterfall-correctness). Every persisted FieldValue MUST carry a non-zero
// Provenance — a "bare" value with no lineage is a G5 violation and is rejected by
// FieldValue.Valid.
type Provenance struct {
	Provider       string     // adapter Name() that produced the value
	ObservedAt     time.Time  // when the producing call completed
	CostCredits    Credits    // credits charged for the producing call (0 if served from idempotency cache)
	Confidence     Confidence // provider-reported (pre-fusion) confidence for this observation
	IdempotencyKey string     // the G2 key of the call that produced this value (audit/replay)
}

// zero reports whether the provenance carries no useful lineage. Used by
// FieldValue.Valid to enforce G5.
func (p Provenance) zero() bool {
	return p.Provider == "" || p.IdempotencyKey == "" || p.ObservedAt.IsZero()
}

// FieldValue is a resolved Field: the value, its fused Confidence, and the winning
// observation's Provenance. Losing observations are retained separately by the store
// (W3C PROV, ADR-0006) — this type is the current best value.
type FieldValue struct {
	Field      Field
	Value      string
	Confidence Confidence
	Prov       Provenance
}

// Valid enforces the G5 invariant at the type boundary: a FieldValue must name a
// canonical Field, carry a non-empty value, and carry real Provenance. The store
// refuses to persist anything for which Valid returns false, so provenance cannot be
// silently dropped.
func (v FieldValue) Valid() bool {
	return v.Field.Valid() && v.Value != "" && !v.Prov.zero()
}
