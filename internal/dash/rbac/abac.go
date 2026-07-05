package rbac

import "github.com/enrichment/waterfall/internal/tenant"

// ABAC is the set of attribute constraints a handler may require in addition to the RBAC
// Decision (doc 05 §1.2). A zero-value field means "no constraint on that attribute".
type ABAC struct {
	Region   string // required region attribute, or "" for any
	PlanTier string // required plan tier, or "" for any
}

// Attribute scope prefixes carried on the Principal (materialized from users.abac /
// tenants.plan_tier at authentication time, doc 05 §1.2).
const (
	regionScopePrefix   = "region:"
	planTierScopePrefix = "plan_tier:"
)

// CheckABAC reports whether p satisfies every non-empty constraint in required. It is
// fail-closed per attribute: a required region/plan the Principal does not carry denies the
// request. All authorization is server-side; this refines, never widens, the RBAC Decision.
func CheckABAC(p tenant.Principal, required ABAC) bool {
	if required.Region != "" && !p.HasScope(regionScopePrefix+required.Region) {
		return false
	}
	if required.PlanTier != "" && !p.HasScope(planTierScopePrefix+required.PlanTier) {
		return false
	}
	return true
}
