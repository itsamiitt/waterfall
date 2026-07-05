package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// --- request bodies ---

// providerWriteReq is the create/PATCH body (snake_case, strictly decoded). Every field is a
// pointer / RawMessage so PATCH touches only supplied columns; op_state is deliberately absent
// (mutated only through the lifecycle actions). attrs is presentation-only (planner-read tunables
// are typed columns, migration 0005 header).
type providerWriteReq struct {
	ID                     *string         `json:"id"`
	DisplayName            *string         `json:"display_name"`
	Category               *string         `json:"category"`
	Description            *string         `json:"description"`
	LogoURL                *string         `json:"logo_url"`
	Status                 *string         `json:"status"`
	ComplianceReviewStatus *string         `json:"compliance_review_status"`
	Visibility             *string         `json:"visibility"`
	Priority               *int64          `json:"priority"`
	BaseURL                *string         `json:"base_url"`
	APIVersion             *string         `json:"api_version"`
	AuthScheme             *string         `json:"auth_scheme"`
	AuthHeader             *string         `json:"auth_header"`
	AuthQueryParam         *string         `json:"auth_query_param"`
	Capabilities           *[]Capability   `json:"capabilities"`
	Region                 *[]string       `json:"region"`
	DocsURL                *string         `json:"docs_url"`
	WebhookConfig          json.RawMessage `json:"webhook_config"`
	BulkAPI                *bool           `json:"bulk_api"`
	BatchAPI               *bool           `json:"batch_api"`
	RetryPolicy            json.RawMessage `json:"retry_policy"`
	TimeoutMS              *int64          `json:"timeout_ms"`
	RateLimitRPM           *int64          `json:"rate_limit_rpm"`
	ConcurrencyLimit       *int64          `json:"concurrency_limit"`
	DailyLimit             *int64          `json:"daily_limit"`
	MonthlyLimit           *int64          `json:"monthly_limit"`
	BreakerThreshold       *int64          `json:"breaker_threshold"`
	BreakerCooldownS       *int64          `json:"breaker_cooldown_s"`
	CreditSync             json.RawMessage `json:"credit_sync"`
	CreditsRemaining       *int64          `json:"credits_remaining"`
	UnitCostCredits        *int64          `json:"unit_cost_credits"`
	CostCurrency           *string         `json:"cost_currency"`
	SLAUptimePct           *float64        `json:"sla_uptime_pct"`
	CorrelationGroup       *string         `json:"correlation_group"`
	SunsetAt               *time.Time      `json:"sunset_at"`
	Tags                   *[]string       `json:"tags"`
	Notes                  *string         `json:"notes"`
	Attrs                  json.RawMessage `json:"attrs"`
}

var validStatuses = map[string]bool{
	StatusActiveCandidate: true, StatusDeprioritized: true, StatusExcluded: true,
}
var validAuthSchemes = map[string]bool{
	"api-key-header": true, "api-key-query": true, "bearer": true, "basic": true, "oauth2-cc": true,
}
var validVisibilities = map[string]bool{
	VisibilityTenantReadable: true, VisibilityPlatformOnly: true,
}

// validate checks the closed enums (CHECK-constraint columns) before hitting the DB so a bad
// value is a clean 422, never a 500 from a constraint violation.
func (req *providerWriteReq) validate() (string, bool) {
	if req.Status != nil && !validStatuses[*req.Status] {
		return "status must be ACTIVE-CANDIDATE, DEPRIORITIZED, or EXCLUDED", false
	}
	if req.AuthScheme != nil && *req.AuthScheme != "" && !validAuthSchemes[*req.AuthScheme] {
		return "auth_scheme is not a recognized scheme", false
	}
	if req.Visibility != nil && !validVisibilities[*req.Visibility] {
		return "visibility must be tenant_readable or platform_only", false
	}
	return "", true
}

// apply appends every supplied column to b (present pointers are forced so an explicit "" clears
// a text column). id is handled by the create path and is never patchable.
func (req *providerWriteReq) apply(b *colsBuilder) {
	if req.DisplayName != nil {
		b.strForce("display_name", *req.DisplayName)
	}
	if req.Category != nil {
		b.strForce("category", *req.Category)
	}
	if req.Description != nil {
		b.strForce("description", *req.Description)
	}
	if req.LogoURL != nil {
		b.strForce("logo_url", *req.LogoURL)
	}
	if req.Status != nil {
		b.strForce("status", *req.Status)
	}
	if req.ComplianceReviewStatus != nil {
		b.strForce("compliance_review_status", *req.ComplianceReviewStatus)
	}
	if req.Visibility != nil {
		b.strForce("visibility", *req.Visibility)
	}
	if req.BaseURL != nil {
		b.strForce("base_url", *req.BaseURL)
	}
	if req.APIVersion != nil {
		b.strForce("api_version", *req.APIVersion)
	}
	if req.AuthScheme != nil {
		b.strForce("auth_scheme", *req.AuthScheme)
	}
	if req.AuthHeader != nil {
		b.strForce("auth_header", *req.AuthHeader)
	}
	if req.AuthQueryParam != nil {
		b.strForce("auth_query_param", *req.AuthQueryParam)
	}
	if req.DocsURL != nil {
		b.strForce("docs_url", *req.DocsURL)
	}
	if req.CostCurrency != nil {
		b.strForce("cost_currency", *req.CostCurrency)
	}
	if req.CorrelationGroup != nil {
		b.strForce("correlation_group", *req.CorrelationGroup)
	}
	if req.Notes != nil {
		b.strForce("notes", *req.Notes)
	}
	b.i64("priority", req.Priority)
	b.i64("timeout_ms", req.TimeoutMS)
	b.i64("rate_limit_rpm", req.RateLimitRPM)
	b.i64("concurrency_limit", req.ConcurrencyLimit)
	b.i64("daily_limit", req.DailyLimit)
	b.i64("monthly_limit", req.MonthlyLimit)
	b.i64("breaker_threshold", req.BreakerThreshold)
	b.i64("breaker_cooldown_s", req.BreakerCooldownS)
	b.i64("credits_remaining", req.CreditsRemaining)
	b.i64("unit_cost_credits", req.UnitCostCredits)
	b.f64("sla_uptime_pct", req.SLAUptimePct)
	b.boolean("bulk_api", req.BulkAPI)
	b.boolean("batch_api", req.BatchAPI)
	b.ts("sunset_at", req.SunsetAt)
	if req.Capabilities != nil {
		b.caps("capabilities", *req.Capabilities)
	}
	if req.Region != nil {
		b.arr("region", *req.Region)
	}
	if req.Tags != nil {
		b.arr("tags", *req.Tags)
	}
	b.jsonb("webhook_config", req.WebhookConfig)
	b.jsonb("retry_policy", req.RetryPolicy)
	b.jsonb("credit_sync", req.CreditSync)
	b.jsonb("attrs", req.Attrs)
}

// actionBody is the optional {"reason": "..."} body carried by lifecycle actions.
type actionBody struct {
	Reason string `json:"reason"`
}

// --- response body ---

// providerDTO is the wire projection of a provider. Operator (full-row) responses populate every
// field; tenant catalog responses leave platform-only fields zero/nil so omitempty drops them.
// effective_available / availability / unavailable_reason are ALWAYS present (the computed axis).
type providerDTO struct {
	ID                     string       `json:"id"`
	DisplayName            string       `json:"display_name"`
	Category               string       `json:"category,omitempty"`
	Description            string       `json:"description,omitempty"`
	LogoURL                string       `json:"logo_url,omitempty"`
	Status                 string       `json:"status"`
	ComplianceReviewStatus string       `json:"compliance_review_status,omitempty"`
	OpState                string       `json:"op_state,omitempty"`
	EffectiveAvailable     bool         `json:"effective_available"`
	Availability           string       `json:"availability"`
	UnavailableReason      *string      `json:"unavailable_reason"`
	Visibility             string       `json:"visibility,omitempty"`
	Priority               *int64       `json:"priority,omitempty"`
	BaseURL                string       `json:"base_url,omitempty"`
	APIVersion             string       `json:"api_version,omitempty"`
	AuthScheme             string       `json:"auth_scheme,omitempty"`
	AuthHeader             string       `json:"auth_header,omitempty"`
	AuthQueryParam         string       `json:"auth_query_param,omitempty"`
	Capabilities           []Capability `json:"capabilities,omitempty"`
	Region                 []string     `json:"region,omitempty"`
	DocsURL                string       `json:"docs_url,omitempty"`
	TimeoutMS              *int64       `json:"timeout_ms,omitempty"`
	RateLimitRPM           *int64       `json:"rate_limit_rpm,omitempty"`
	ConcurrencyLimit       *int64       `json:"concurrency_limit,omitempty"`
	BreakerThreshold       *int64       `json:"breaker_threshold,omitempty"`
	BreakerCooldownS       *int64       `json:"breaker_cooldown_s,omitempty"`
	CreditsRemaining       *int64       `json:"credits_remaining,omitempty"`
	UnitCostCredits        *int64       `json:"unit_cost_credits,omitempty"`
	HealthScore            *float64     `json:"health_score,omitempty"`
	CostScore              *float64     `json:"cost_score,omitempty"`
	SuccessScore           *float64     `json:"success_score,omitempty"`
	ConfidenceScore        *float64     `json:"confidence_score,omitempty"`
	PerformanceScore       *float64     `json:"performance_score,omitempty"`
	AvgLatencyMS           *float64     `json:"avg_latency_ms,omitempty"`
	Tags                   []string     `json:"tags,omitempty"`
	SunsetAt               *time.Time   `json:"sunset_at,omitempty"`
	LastHealthAt           *time.Time   `json:"last_health_at,omitempty"`
	LastSyncAt             *time.Time   `json:"last_sync_at,omitempty"`
	ArchivedAt             *time.Time   `json:"archived_at,omitempty"`
	CreatedAt              *time.Time   `json:"created_at,omitempty"`
	UpdatedAt              *time.Time   `json:"updated_at,omitempty"`
}

// effectiveOf computes availability from a provider. The tenant catalog projection does not carry
// op_state (migration 0005 view omits it), so a projected row (op_state == "") is treated as
// enabled and effective_available reflects the inclusion-status conjunct only; the full
// status×op_state conjunction is computed for operator (full-row) responses. (Open item: projecting
// op_state into providers_catalog would let tenants see the full conjunction — doc 04 §2.3 OI.)
func effectiveOf(p Provider) Availability {
	if p.OpState == "" {
		p.OpState = OpEnabled
	}
	return EffectiveAvailability(p)
}

func toDTO(p Provider) providerDTO {
	av := effectiveOf(p)
	return providerDTO{
		ID:                     p.ID,
		DisplayName:            p.DisplayName,
		Category:               p.Category,
		Description:            p.Description,
		LogoURL:                p.LogoURL,
		Status:                 p.Status,
		ComplianceReviewStatus: p.ComplianceReviewStatus,
		OpState:                p.OpState,
		EffectiveAvailable:     av.Available(),
		Availability:           string(av.State),
		UnavailableReason:      reasonOut(av.Reason),
		Visibility:             p.Visibility,
		Priority:               p.Priority,
		BaseURL:                p.BaseURL,
		APIVersion:             p.APIVersion,
		AuthScheme:             p.AuthScheme,
		AuthHeader:             p.AuthHeader,
		AuthQueryParam:         p.AuthQueryParam,
		Capabilities:           p.Capabilities,
		Region:                 p.Region,
		DocsURL:                p.DocsURL,
		TimeoutMS:              p.TimeoutMS,
		RateLimitRPM:           p.RateLimitRPM,
		ConcurrencyLimit:       p.ConcurrencyLimit,
		BreakerThreshold:       p.BreakerThreshold,
		BreakerCooldownS:       p.BreakerCooldownS,
		CreditsRemaining:       p.CreditsRemaining,
		UnitCostCredits:        p.UnitCostCredits,
		HealthScore:            p.HealthScore,
		CostScore:              p.CostScore,
		SuccessScore:           p.SuccessScore,
		ConfidenceScore:        p.ConfidenceScore,
		PerformanceScore:       p.PerformanceScore,
		AvgLatencyMS:           p.AvgLatencyMS,
		Tags:                   p.Tags,
		SunsetAt:               p.SunsetAt,
		LastHealthAt:           p.LastHealthAt,
		LastSyncAt:             p.LastSyncAt,
		ArchivedAt:             p.ArchivedAt,
		CreatedAt:              p.CreatedAt,
		UpdatedAt:              p.UpdatedAt,
	}
}

// --- request parsing helpers ---

// decodeJSON strictly decodes a required JSON body (DisallowUnknownFields + MaxBytesReader),
// writing 400 invalid_json and returning false on any failure (doc 04 §1.1).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

// optionalJSON decodes an OPTIONAL body: an empty body is fine (returns nil); a present body is
// strictly decoded. Used by lifecycle actions whose {"reason":...} is optional.
func optionalJSON(r *http.Request, dst any) error {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// parseCursor decodes ?cursor= (opaque base64url); a bad cursor is 400 invalid_cursor.
func parseCursor(w http.ResponseWriter, r *http.Request) (db.Cursor, bool) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return db.Cursor{}, false
	}
	return cur, true
}

// parseLimit decodes ?limit=; out-of-range/non-integer is 400 invalid_filter (still clamped by
// db.ClampLimit downstream).
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be between 1 and 200")
		return 0, false
	}
	return n, true
}

// parseFilter reads the closed set of List predicates from the query string.
func parseFilter(r *http.Request) Filter {
	q := r.URL.Query()
	return Filter{
		Status:   q.Get("status"),
		OpState:  q.Get("op_state"),
		Category: q.Get("category"),
		Q:        q.Get("q"),
		Region:   q.Get("region"),
		Tag:      q.Get("tag"),
	}
}

// encodeCursor renders a non-empty keyset cursor as an opaque token, or "" for the last page.
func encodeCursor(c db.Cursor) string {
	if len(c.K) == 0 && c.ID == "" {
		return ""
	}
	return db.EncodeCursor(c)
}

// splitIDs parses a comma-separated id list, trimming blanks.
func splitIDs(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
