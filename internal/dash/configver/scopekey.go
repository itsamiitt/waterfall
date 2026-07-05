package configver

import (
	"regexp"
	"strings"
)

// scope_key grammar (doc 07 §3.1, pins OI-API-3). The tenant dimension is carried by row
// tenancy, NOT the key; scope_key encodes only product/country, in fixed order, "+"-joined:
//
//	scope_key := "default" | dim | "product:" slug "+" "country:" alpha2
//	dim       := "product:" slug | "country:" alpha2
//	slug      := lowercase [a-z0-9-]{1,64}
//	alpha2    := uppercase ISO 3166-1 alpha-2
var (
	slugRe   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	alpha2Re = regexp.MustCompile(`^[A-Z]{2}$`)
)

// ScopeDims is the parsed product/country dimensions of a scope_key (tenant is row tenancy).
type ScopeDims struct {
	Product string // "" when absent
	Country string // "" when absent
}

// ValidScopeKey reports whether s is a well-formed scope_key per the doc 07 §3.1 grammar.
func ValidScopeKey(s string) bool {
	_, ok := ParseScopeKey(s)
	return ok
}

// ParseScopeKey parses a scope_key into its dimensions. "default" yields the zero ScopeDims.
// A malformed key returns ok=false (the caller maps it to 400 invalid_scope_key).
func ParseScopeKey(s string) (ScopeDims, bool) {
	if s == "default" {
		return ScopeDims{}, true
	}
	parts := strings.Split(s, "+")
	var d ScopeDims
	switch len(parts) {
	case 1:
		return parseDim(parts[0], ScopeDims{})
	case 2:
		// Fixed order: product then country.
		mid, ok := parseDim(parts[0], d)
		if !ok || mid.Product == "" {
			return ScopeDims{}, false
		}
		full, ok := parseDim(parts[1], mid)
		if !ok || full.Country == "" {
			return ScopeDims{}, false
		}
		return full, true
	default:
		return ScopeDims{}, false
	}
}

// parseDim parses one "product:<slug>" or "country:<alpha2>" dimension into d, rejecting a
// dimension already set (so "product:a+product:b" is invalid).
func parseDim(seg string, d ScopeDims) (ScopeDims, bool) {
	switch {
	case strings.HasPrefix(seg, "product:"):
		v := strings.TrimPrefix(seg, "product:")
		if d.Product != "" || !slugRe.MatchString(v) {
			return ScopeDims{}, false
		}
		d.Product = v
		return d, true
	case strings.HasPrefix(seg, "country:"):
		v := strings.TrimPrefix(seg, "country:")
		if d.Country != "" || !alpha2Re.MatchString(v) {
			return ScopeDims{}, false
		}
		d.Country = v
		return d, true
	default:
		return ScopeDims{}, false
	}
}
