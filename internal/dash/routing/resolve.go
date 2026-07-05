// Package routing is the Request Routing Center (module 6, doc 07): a thin service over the shared
// configver lifecycle engine with the routing_policy VR validators, the 8-level most-specific-wins
// scope-precedence resolver, and the read-only dry-run simulator. It owns NO lifecycle logic —
// draft/validate/publish/rollback all live in configver — only the routing_policy payload semantics.
//
// The tri-state override doctrine (MASTER SPEC §10b, doc 07 §2): per-Provider overrides are
// inherit/off/on — `inherit` is transparent to precedence resolution; a nullable bool is
// deliberately not used because it cannot distinguish "no opinion" from "explicitly off". Lists are
// atomic under resolution — never merged element-wise.
package routing

import "github.com/enrichment/waterfall/internal/dash/configver"

// ScopeLevel is one of the 8 precedence levels (doc 07 §3.2), 1 = most specific. Tenant specificity
// always outranks platform specificity, so all four tenant levels precede all four platform levels.
type ScopeLevel int

const (
	LevelTenantProductCountry   ScopeLevel = iota + 1 // 1: tenant row, product:P+country:C
	LevelTenantProduct                                // 2: tenant row, product:P
	LevelTenantCountry                                // 3: tenant row, country:C
	LevelTenantDefault                                // 4: tenant row, default
	LevelPlatformProductCountry                       // 5: platform row, product:P+country:C
	LevelPlatformProduct                              // 6: platform row, product:P
	LevelPlatformCountry                              // 7: platform row, country:C
	LevelPlatformDefault                              // 8: platform row, default
)

// levelOrder is the fixed 1->8 walk order (a total order; §3.2 needs no tie-break).
var levelOrder = []ScopeLevel{
	LevelTenantProductCountry, LevelTenantProduct, LevelTenantCountry, LevelTenantDefault,
	LevelPlatformProductCountry, LevelPlatformProduct, LevelPlatformCountry, LevelPlatformDefault,
}

// String renders a level as its provenance label (shown in the dry-run resolved_scope).
func (l ScopeLevel) String() string {
	switch l {
	case LevelTenantProductCountry:
		return "tenant+product+country"
	case LevelTenantProduct:
		return "tenant+product"
	case LevelTenantCountry:
		return "tenant+country"
	case LevelTenantDefault:
		return "tenant"
	case LevelPlatformProductCountry:
		return "product+country"
	case LevelPlatformProduct:
		return "product"
	case LevelPlatformCountry:
		return "country"
	case LevelPlatformDefault:
		return "default"
	default:
		return "engine_default"
	}
}

// Override is a per-Provider tri-state routing override (doc 07 §2).
type Override struct {
	Mode     string `json:"mode"` // inherit | off | on
	Priority *int   `json:"priority,omitempty"`
	KeyPool  string `json:"key_pool,omitempty"`
}

// Thresholds are the routing_policy Confidence/cost bounds (doc 07 §2).
type Thresholds struct {
	ConfidenceTarget        *float64 `json:"confidence_target,omitempty"`
	MinConfidence           *float64 `json:"min_confidence,omitempty"`
	MaxCostCreditsPerField  *int64   `json:"max_cost_credits_per_field,omitempty"`
	MaxCostCreditsPerRecord *int64   `json:"max_cost_credits_per_record,omitempty"`
}

// Waterfall is the routing_policy explicit ordering block (doc 07 §2).
type Waterfall struct {
	Order            []string       `json:"order,omitempty"`
	ParallelGroup    *ParallelGroup `json:"parallel_group,omitempty"`
	SequentialChains [][]string     `json:"sequential_chains,omitempty"`
	RetryOrder       []string       `json:"retry_order,omitempty"`
	FailoverOrder    []string       `json:"failover_order,omitempty"`
}

// ParallelGroup is the bounded cheap prefix fanned out concurrently (ADR-0007).
type ParallelGroup struct {
	Providers []string `json:"providers"`
}

// Scope echoes the scope dimensions the row is keyed on (VR-14).
type Scope struct {
	Tenant  string `json:"tenant,omitempty"`
	Product string `json:"product,omitempty"`
	Country string `json:"country,omitempty"`
}

// Policy is the parsed routing_policy payload (doc 07 §2 JSON Schema).
type Policy struct {
	SchemaVersion     int                 `json:"schema_version"`
	Scope             Scope               `json:"scope"`
	ProviderOverrides map[string]Override `json:"provider_overrides"`
	Waterfall         Waterfall           `json:"waterfall"`
	Thresholds        Thresholds          `json:"thresholds"`
}

// Resolved carries an effective setting value together with the source level that supplied it, so
// the UI and dry-run can show provenance and never re-derive it (doc 07 §3.2).
type Resolved[T any] struct {
	Value  T          `json:"value"`
	Source ScopeLevel `json:"-"`
}

// ResolvedMode is an effective per-Provider override with its source level.
type ResolvedMode struct {
	Mode     string     `json:"mode"`
	Priority *int       `json:"priority,omitempty"`
	KeyPool  string     `json:"key_pool,omitempty"`
	Source   ScopeLevel `json:"-"`
}

// Effective is the folded routing configuration for a request (doc 07 §3.2). A nil pointer means
// the setting was defined at no consulted level (the engine's built-in default applies, reported as
// source engine_default).
type Effective struct {
	ConfidenceTarget        *Resolved[float64]
	MinConfidence           *Resolved[float64]
	MaxCostCreditsPerField  *Resolved[int64]
	MaxCostCreditsPerRecord *Resolved[int64]
	Order                   *Resolved[[]string]
	ProviderModes           map[string]ResolvedMode
}

// Resolve folds up to eight active routing policies most-specific-wins (doc 07 §3.2). Scalars and
// lists are ATOMIC — the first level (1->8) that defines a setting supplies it entirely; lists are
// never merged element-wise. provider_overrides fold PER Provider: walk 1->8 and take the first
// entry whose mode is not "inherit" (inherit is transparent). Every resolved setting carries its
// source level. Resolve is a PURE function of its inputs — the "computed in exactly one place"
// doctrine — reused by the API resolver, the dry-run simulator, and the engine config reader.
func Resolve(dims configver.ScopeDims, active map[ScopeLevel]Policy) Effective {
	_ = dims // the map already encodes which levels are applicable for these dims.
	var eff Effective
	eff.ProviderModes = map[string]ResolvedMode{}

	for _, lvl := range levelOrder {
		p, ok := active[lvl]
		if !ok {
			continue
		}
		// Scalars + lists: first definer wins (atomic).
		if eff.ConfidenceTarget == nil && p.Thresholds.ConfidenceTarget != nil {
			eff.ConfidenceTarget = &Resolved[float64]{Value: *p.Thresholds.ConfidenceTarget, Source: lvl}
		}
		if eff.MinConfidence == nil && p.Thresholds.MinConfidence != nil {
			eff.MinConfidence = &Resolved[float64]{Value: *p.Thresholds.MinConfidence, Source: lvl}
		}
		if eff.MaxCostCreditsPerField == nil && p.Thresholds.MaxCostCreditsPerField != nil {
			eff.MaxCostCreditsPerField = &Resolved[int64]{Value: *p.Thresholds.MaxCostCreditsPerField, Source: lvl}
		}
		if eff.MaxCostCreditsPerRecord == nil && p.Thresholds.MaxCostCreditsPerRecord != nil {
			eff.MaxCostCreditsPerRecord = &Resolved[int64]{Value: *p.Thresholds.MaxCostCreditsPerRecord, Source: lvl}
		}
		if eff.Order == nil && len(p.Waterfall.Order) > 0 {
			eff.Order = &Resolved[[]string]{Value: p.Waterfall.Order, Source: lvl}
		}
		// provider_overrides: per-Provider first non-inherit wins.
		for id, ov := range p.ProviderOverrides {
			if _, decided := eff.ProviderModes[id]; decided {
				continue
			}
			if ov.Mode == "" || ov.Mode == "inherit" {
				continue // transparent — keep walking
			}
			eff.ProviderModes[id] = ResolvedMode{Mode: ov.Mode, Priority: ov.Priority, KeyPool: ov.KeyPool, Source: lvl}
		}
	}
	return eff
}
