package configver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
)

// Finding is one uniform, machine-readable validation report entry (doc 07 §5). Message shape:
// lowercase sentence, names the offending object and the violated bound, no PII, no secrets.
type Finding struct {
	Rule     string `json:"rule"`
	Code     string `json:"code"`
	Severity string `json:"severity"` // "error" | "warning"
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// Severity constants.
const (
	SevError   = "error"
	SevWarning = "warning"
)

// Capability is the non-secret {field, cost, expected_confidence} advertised by a Provider — the
// planner priors the dry-run simulator reads (doc 07 §7).
type Capability struct {
	Field              string
	Cost               int64
	ExpectedConfidence float64
}

// ProviderInfo is the non-secret catalog metadata a validator reads about a referenced Provider
// (doc 07 §5 VR-1..VR-4, VR-12, VR-15) plus its capabilities for the dry-run planner (§7).
type ProviderInfo struct {
	ID           string
	Status       string // ACTIVE-CANDIDATE | DEPRIORITIZED | EXCLUDED (ADR-0009 trichotomy)
	OpState      string // enabled | disabled | paused | maintenance
	Compliance   string // compliance_review_status; "approved" unlocks DEPRIORITIZED (VR-3)
	SunsetAt     *time.Time
	Capabilities []Capability
}

// HasCapability reports whether the Provider advertises field (VR-15 provider_no_capability).
func (p ProviderInfo) HasCapability(field string) bool {
	for _, c := range p.Capabilities {
		if c.Field == field {
			return true
		}
	}
	return false
}

// ProviderSource looks up a Provider's catalog metadata by id (satisfied by an adapter over
// providers.Store in cmd/dashboardd; a fake in tests). Reads are non-secret catalog fields.
type ProviderSource interface {
	Lookup(ctx context.Context, id string) (ProviderInfo, bool, error)
}

// BudgetSource returns the applicable Tenant budget limit_credits for a (scope, scope_key, period)
// key (VR-7). Satisfied by NewBudgetReader over db.Store.
type BudgetSource interface {
	Limit(ctx context.Context, scope, scopeKey, period string) (limitCredits int64, found bool, err error)
}

// Checker accumulates validation Findings for one payload against the VR catalog (doc 07 §5). It
// is payload-shape-agnostic — routing / workflows extract the referenced Providers, ordering
// edges, thresholds, and cost bounds and drive these methods — so the rule logic lives in one
// place. A non-nil Fault() is an internal error (e.g. a Provider lookup failed), NEVER a failed
// rule (a failed rule is report content, doc 07 §5).
type Checker struct {
	ctx       context.Context
	providers ProviderSource
	budgets   BudgetSource
	now       time.Time
	findings  []Finding
	fault     error
}

// NewChecker builds a Checker bound to ctx (so RLS-scoped budget reads use the caller's Principal).
func NewChecker(ctx context.Context, providers ProviderSource, budgets BudgetSource, now time.Time) *Checker {
	return &Checker{ctx: ctx, providers: providers, budgets: budgets, now: now}
}

func (c *Checker) add(rule, code, sev, path, msg string) {
	c.findings = append(c.findings, Finding{Rule: rule, Code: code, Severity: sev, Path: path, Message: msg})
}

// Error records an error-severity Finding (for rules a caller checks itself, e.g. VR-14).
func (c *Checker) Error(rule, code, path, msg string) { c.add(rule, code, SevError, path, msg) }

// Warn records a warning-severity Finding.
func (c *Checker) Warn(rule, code, path, msg string) { c.add(rule, code, SevWarning, path, msg) }

// Fault returns the first internal error encountered (Provider/budget lookup), or nil.
func (c *Checker) Fault() error { return c.fault }

// HasErrors reports whether any error-severity Finding was recorded.
func (c *Checker) HasErrors() bool {
	for _, f := range c.findings {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}

// Report marshals the accumulated findings into the validator report shape the Service augments:
// {"errors":[...],"warnings":[...]}.
func (c *Checker) Report() json.RawMessage {
	errs := []Finding{}
	warns := []Finding{}
	for _, f := range c.findings {
		if f.Severity == SevError {
			errs = append(errs, f)
		} else {
			warns = append(warns, f)
		}
	}
	b, _ := json.Marshal(map[string]any{"errors": errs, "warnings": warns})
	return b
}

// Provider checks a single referenced Provider id (VR-1/VR-2/VR-3/VR-4/VR-12) and returns its
// info. onMode is the tri-state routing override mode for this reference ("" for workflow refs);
// a mode of "on" for an EXCLUDED Provider is equally rejected (VR-2). It records findings and
// returns (info, exists).
func (c *Checker) Provider(path, id, onMode string) (ProviderInfo, bool) {
	if c.providers == nil {
		return ProviderInfo{}, false
	}
	info, ok, err := c.providers.Lookup(c.ctx, id)
	if err != nil {
		if c.fault == nil {
			c.fault = err
		}
		return ProviderInfo{}, false
	}
	if !ok {
		c.add("VR-1", "provider_unknown", SevError, path,
			fmt.Sprintf("provider %s is not in the catalog", id))
		return ProviderInfo{}, false
	}
	switch info.Status {
	case "EXCLUDED":
		c.add("VR-2", "provider_excluded", SevError, path,
			fmt.Sprintf("provider %s has inclusion status EXCLUDED and cannot be referenced", id))
	case "DEPRIORITIZED":
		if info.Compliance != "approved" {
			c.add("VR-3", "provider_compliance_unreviewed", SevError, path,
				fmt.Sprintf("provider %s is DEPRIORITIZED and may be referenced only when compliance_review_status is approved (is %q)",
					id, info.Compliance))
		}
	}
	switch info.OpState {
	case "disabled", "paused":
		c.add("VR-4", "provider_op_state_blocked", SevError, path,
			fmt.Sprintf("provider %s runtime op_state is %s and cannot serve", id, info.OpState))
	case "maintenance":
		c.add("VR-4", "provider_in_maintenance", SevWarning, path,
			fmt.Sprintf("provider %s is in maintenance", id))
	}
	if info.SunsetAt != nil {
		if !info.SunsetAt.After(c.now) {
			c.add("VR-12", "provider_sunset", SevError, path,
				fmt.Sprintf("provider %s is past its sunset date and cannot be referenced", id))
		} else if info.SunsetAt.Before(c.now.Add(30 * 24 * time.Hour)) {
			c.add("VR-12", "provider_sunsetting", SevWarning, path,
				fmt.Sprintf("provider %s is sunsetting within 30 days", id))
		}
	}
	return info, true
}

// Confidence enforces a [0,1] range on a Confidence-typed value (VR-8). A nil value is skipped.
func (c *Checker) Confidence(path string, v *float64) {
	if v == nil {
		return
	}
	if *v < 0 || *v > 1 {
		c.add("VR-8", "threshold_out_of_range", SevError, path,
			fmt.Sprintf("confidence value %v is outside the allowed range [0,1]", *v))
	}
}

// MaxCost enforces that a per-Job/per-record spend bound does not exceed the applicable Tenant
// budget (VR-7). The error names BOTH numbers. A nil value or a missing budget row skips the
// check (config may always tighten below an absent budget).
func (c *Checker) MaxCost(path string, credits *int64, budgetScope, budgetScopeKey, period string) {
	if credits == nil || c.budgets == nil {
		return
	}
	limit, found, err := c.budgets.Limit(c.ctx, budgetScope, budgetScopeKey, period)
	if err != nil {
		if c.fault == nil {
			c.fault = err
		}
		return
	}
	if !found {
		return
	}
	if *credits > limit {
		c.add("VR-7", "cost_exceeds_budget", SevError, path,
			fmt.Sprintf("max_cost_credits %d exceeds the tenant %s budget of %d credits (%s)",
				*credits, budgetScope, limit, period))
	}
}

// Acyclic checks that the union of ordering constructs forms a directed ACYCLIC graph (VR-5).
// Each edge list is a strict order: consecutive elements form directed edges (a->b->c). A cycle
// anywhere across all lists is a single error at path.
func (c *Checker) Acyclic(path string, edgeLists ...[]string) {
	adj := map[string][]string{}
	for _, list := range edgeLists {
		for i := 0; i+1 < len(list); i++ {
			adj[list[i]] = append(adj[list[i]], list[i+1])
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		for _, m := range adj[n] {
			switch color[m] {
			case gray:
				return true // back-edge => cycle
			case white:
				if dfs(m) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for n := range adj {
		if color[n] == white {
			if dfs(n) {
				c.add("VR-5", "waterfall_cycle", SevError, path,
					"the provider ordering forms a cycle across order / sequential / retry / failover constructs")
				return
			}
		}
	}
}

// FieldVocabulary checks that each declared target Field is canonical (VR-15 field_unknown).
func (c *Checker) FieldVocabulary(path string, fields []string) {
	for i, f := range fields {
		if !domain.Field(f).Valid() {
			c.add("VR-15", "field_unknown", SevError, fmt.Sprintf("%s/%d", path, i),
				fmt.Sprintf("field %q is not in the canonical Field vocabulary", f))
		}
	}
}

// GateOverride rejects any payload key that would attempt to loosen the G3 bounded-execution or
// G4 cost-ceiling gates (VR-16 catch-all gate_override_rejected). The engine re-checks G3/G4 at
// call time regardless, but a config author must never be able to express the intent to bypass
// them, so these keys are refused outright.
func (c *Checker) GateOverride(payload json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil {
		return
	}
	for _, k := range []string{
		"bypass_g3", "bypass_g4", "override_cost_ceiling", "disable_cost_ceiling",
		"unbounded", "ignore_ceiling", "unlimited_cost", "disable_bounds",
	} {
		if raw, ok := m[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil && b {
				c.add("VR-16", "gate_override_rejected", SevError, "/"+k,
					fmt.Sprintf("payload key %q attempts to override an engine gate (G3/G4) and is rejected", k))
			}
		}
	}
}

// Duplicate flags a repeated Provider within a single ordering list (VR-16 duplicate_provider).
func (c *Checker) Duplicate(path string, list []string) {
	seen := map[string]bool{}
	for i, id := range list {
		if seen[id] {
			c.add("VR-16", "duplicate_provider", SevError, fmt.Sprintf("%s/%d", path, i),
				fmt.Sprintf("provider %s appears more than once in %s", id, path))
		}
		seen[id] = true
	}
}
