package rotation

import (
	"context"

	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// UsageSample is one leased Provider call's telemetry dimensions, emitted from Lease.Done through
// the optional Config.RecordUsage hook (OI-P4-1, doc 12 §P4). It is a plain value type — the
// rotation engine deliberately does NOT import internal/dash/telemetry — so the orchestrator can
// adapt RecordUsage onto a telemetry.Recorder (UsageSample fields map 1:1 onto telemetry.UsageEvent)
// in dashboardd without rotation taking a telemetry dependency (or an import cycle).
//
// Dimensions are captured at Lease() time from the request context (tenant/workflow_key/country)
// and completed in Done() from the key + Outcome (key_id/provider_id/credits/outcome_class/latency).
type UsageSample struct {
	TenantID     string // owning Tenant; "platform" for dashboard-initiated leases (health/test/bench)
	KeyID        string // leased Provider Key id (G5 provenance)
	ProviderID   string // Provider that served the call, derived from the pool selector
	WorkflowKey  string // workflow attribution from the lease ctx, or "" (see attribution caveat)
	Country      string // subject country from the lease ctx, or ""
	OutcomeClass string // "ok" on success, else the 8-class taxonomy string (domain.ErrorClass)
	Credits      int64  // credits spent, from the key's cost_per_call
	LatMs        int    // observed latency in milliseconds
}

// platformTenant is the tenant a lease is attributed to when the request context carries no
// authenticated principal — i.e. dashboard-initiated calls (health-check / key test / benchmark),
// which are platform-owned rather than customer traffic.
const platformTenant = "platform"

// usageOK is the success sentinel for UsageSample.OutcomeClass; it mirrors telemetry.OutcomeOK
// ("ok") so the orchestrator's adapter is a straight field copy. Kept as a local constant so
// rotation stays free of the telemetry import.
const usageOK = "ok"

// usageDims are the request-scoped attribution values snapshotted ONCE at Lease() time — the
// tenant/workflow/country live on the lease context, which is not available (with request scope)
// inside a Done callback invoked later on the egress path.
type usageDims struct {
	tenantID    string
	providerID  string
	workflowKey string
	country     string
}

// captureUsage snapshots the attribution dimensions from the lease context. The tenant comes ONLY
// from the authenticated principal (G1, never a payload field); absent one it defaults to the
// platform Tenant. provider_id is derived from the pool selector (provider_id:name).
func captureUsage(ctx context.Context, poolSelector string) usageDims {
	tid := platformTenant
	if id, err := tenant.TenantID(ctx); err == nil && id != "" {
		tid = id
	}
	providerID, _, ok := splitSelector(poolSelector)
	if !ok {
		providerID = poolSelector
	}
	return usageDims{
		tenantID:    tid,
		providerID:  providerID,
		workflowKey: workflowFromContext(ctx),
		country:     countryFromContext(ctx),
	}
}

// outcomeClass maps a provider Outcome onto the usage_events outcome_class vocabulary: "ok" on a
// 2xx success, otherwise the taxonomy class string (AUTH, RATE_LIMIT, ... UNKNOWN).
func outcomeClass(o provider.Outcome) string {
	if o.OK {
		return usageOK
	}
	return o.Class.String()
}

// --- optional lease-context dimensions ---
//
// enrichd tags the lease context with the workflow_key and subject country of the Enrichment Job
// so a real request's usage row is fully attributed. Dashboard-initiated leases leave them unset
// (they carry no workflow), so both are optional and default to "".
//
// The ctx contract itself lives in internal/provider (a dependency-free leaf), so the enrichment
// engine can SET attribution ahead of the provider/lease call without importing the dashboard
// rotation stack. These are thin re-exports so callers already holding a rotation import (and the
// existing rotation tests) keep the rotation.With* API; captureUsage reads the same provider keys.

// WithAttribution tags ctx with BOTH the workflow_key and subject country of the enrichment work
// driving the lease, so a real request's usage row is fully attributed (OI-P4-1b). captureUsage
// snapshots the pair at Lease() time and Done() emits them on the UsageSample. Empty strings are
// carried as "" (unattributed), so it is backward-compatible.
func WithAttribution(ctx context.Context, workflowKey, country string) context.Context {
	return provider.WithAttribution(ctx, workflowKey, country)
}

// AttributionFromContext reads back the workflow_key/country pair set by WithAttribution (or the
// individual setters); both default to "" when unset.
func AttributionFromContext(ctx context.Context) (workflowKey, country string) {
	return provider.AttributionFromContext(ctx)
}

// WithWorkflowKey tags ctx with the workflow_key attribution carried into the lease.
func WithWorkflowKey(ctx context.Context, workflowKey string) context.Context {
	return provider.WithWorkflowKey(ctx, workflowKey)
}

// WithCountry tags ctx with the subject country attribution carried into the lease.
func WithCountry(ctx context.Context, country string) context.Context {
	return provider.WithCountry(ctx, country)
}

func workflowFromContext(ctx context.Context) string {
	w, _ := provider.AttributionFromContext(ctx)
	return w
}

func countryFromContext(ctx context.Context) string {
	_, c := provider.AttributionFromContext(ctx)
	return c
}
