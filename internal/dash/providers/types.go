// Package providers is the dashboard's Provider Management module (doc 00 §2.1 M2, doc 04
// §2.3): CRUD and lifecycle over the platform-owned provider catalog (migration 0005), plus
// the SINGLE source of truth for effective availability.
//
// Two orthogonal state axes live on every provider row and must never be conflated:
//
//   - status — the ADR-0009 inclusion trichotomy (ACTIVE-CANDIDATE / DEPRIORITIZED /
//     EXCLUDED): a catalog-lifecycle judgement about whether the connector may be used at
//     all, gated by compliance review.
//   - op_state — the runtime operational switch (enabled / disabled / paused / maintenance):
//     an operator's live on/off control.
//
// Effective availability is the CONJUNCTION of the two, COMPUTED by EffectiveAvailability and
// never stored (migration 0005 header; doc 04 §2.3 "computed server-side, one function").
//
// Gates / invariants:
//   - G1 tenant isolation. Providers are Class P (platform-owned). Operator reads/writes run
//     through db.Store.PlatformTx; tenant reads run through the enumerated providers_catalog
//     projection under the caller's own Principal — never a body-supplied tenant.
//   - Bounded queries. List is cursor-paginated under db.ClampLimit (default 50 / cap 200).
//   - No secrets in responses/logs. The catalog row holds auth *descriptors* (scheme + header
//     name), never key material; secrets live only in secret_envelopes via internal/dash/secrets.
package providers

import (
	"encoding/json"
	"errors"
	"time"
)

// Inclusion status vocabulary (ADR-0009 trichotomy; migration 0005 CHECK).
const (
	StatusActiveCandidate = "ACTIVE-CANDIDATE"
	StatusDeprioritized   = "DEPRIORITIZED"
	StatusExcluded        = "EXCLUDED"
)

// Runtime op_state vocabulary (migration 0005 CHECK).
const (
	OpEnabled     = "enabled"
	OpDisabled    = "disabled"
	OpPaused      = "paused"
	OpMaintenance = "maintenance"
)

// visibility values (migration 0005). tenant_readable rows are exposed through the
// providers_catalog projection; anything else is platform-only.
const (
	VisibilityTenantReadable = "tenant_readable"
	VisibilityPlatformOnly   = "platform_only"
)

// Sentinel errors (wrapped with %w by callers; the HTTP layer maps them to status codes).
var (
	// ErrNotFound reports that no provider exists for the id in the caller's visibility scope.
	ErrNotFound = errors.New("providers: not found")
	// ErrConflict reports a slug collision on create/duplicate (Postgres 23505).
	ErrConflict = errors.New("providers: id already exists")
	// ErrInvalidTransition reports an op_state change the transition guard rejects.
	ErrInvalidTransition = errors.New("providers: invalid op_state transition")
	// ErrValidation reports a malformed request the service refuses (mapped to 422).
	ErrValidation = errors.New("providers: validation failed")
	// ErrNoKey reports that a test/health/benchmark probe had no leasable key (not a crash).
	ErrNoKey = errors.New("providers: no provider key available for probe")
)

// Capability advertises that a provider can return a Field with a declared cost and prior
// expected confidence. It is the JSON (snake_case) mirror of provider.Capability and the
// element type of the providers.capabilities jsonb column.
type Capability struct {
	Field              string  `json:"field"`
	CostCredits        int64   `json:"cost_credits"`
	ExpectedConfidence float64 `json:"expected_confidence"`
}

// Provider is the typed read model of one providers row (migration 0005 §2.2). Nullable
// numeric/timestamp columns are pointers so a genuine NULL is distinguishable from a zero
// (a NULL health_score must not rank as 0); jsonb columns are json.RawMessage except
// capabilities, which is decoded into the typed slice the router/coverage build on.
type Provider struct {
	ID                     string
	DisplayName            string
	Category               string
	Description            string
	LogoURL                string
	Status                 string
	ComplianceReviewStatus string
	OpState                string
	Visibility             string
	Priority               *int64
	BaseURL                string
	APIVersion             string
	AuthScheme             string
	AuthHeader             string
	AuthQueryParam         string
	Capabilities           []Capability
	Region                 []string
	DocsURL                string
	WebhookConfig          json.RawMessage
	BulkAPI                *bool
	BatchAPI               *bool
	RetryPolicy            json.RawMessage
	TimeoutMS              *int64
	RateLimitRPM           *int64
	ConcurrencyLimit       *int64
	DailyLimit             *int64
	MonthlyLimit           *int64
	BreakerThreshold       *int64
	BreakerCooldownS       *int64
	CreditSync             json.RawMessage
	CreditsRemaining       *int64
	UnitCostCredits        *int64
	CostCurrency           string
	SLAUptimePct           *float64
	CorrelationGroup       string
	SunsetAt               *time.Time
	ConfidenceScore        *float64
	CostScore              *float64
	PerformanceScore       *float64
	SuccessScore           *float64
	FailureScore           *float64
	HealthScore            *float64
	AvgLatencyMS           *float64
	LastHealthAt           *time.Time
	LastFailureAt          *time.Time
	LastSuccessAt          *time.Time
	LastSyncAt             *time.Time
	Tags                   []string
	Notes                  string
	Attrs                  json.RawMessage
	ArchivedAt             *time.Time
	CreatedAt              *time.Time
	UpdatedAt              *time.Time
	UpdatedBy              string
}

// Filter is the closed set of List predicates (doc 04 §2.3). Empty fields are not applied.
type Filter struct {
	Status   string // exact inclusion status
	OpState  string // exact runtime op_state (ignored on the tenant catalog projection)
	Category string // exact category
	Q        string // display-name / id prefix (case-insensitive)
	Region   string // membership in the region[] array
	Tag      string // membership in the tags[] array
}
