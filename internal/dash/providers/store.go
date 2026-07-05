package providers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// Store is the persistence seam the service depends on (consumer-side interface, satisfied by
// PGStore). Full-row methods run under db.Store.PlatformTx (Class P platform ownership);
// catalog methods run under the caller's own Principal against the providers_catalog projection.
type Store interface {
	Insert(ctx context.Context, cols []colVal) (Provider, error)
	Update(ctx context.Context, id string, cols []colVal) (Provider, error)
	Delete(ctx context.Context, id string) (bool, error)

	GetFull(ctx context.Context, id string) (Provider, error)
	ListFull(ctx context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error)
	GetManyFull(ctx context.Context, ids []string) ([]Provider, error)

	GetCatalog(ctx context.Context, id string) (Provider, error)
	ListCatalog(ctx context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error)
}

// colVal is one column assignment for an INSERT/UPDATE. cast is "" for scalars, or the
// Postgres type to cast a text-encoded value to ("jsonb", "text[]"). Column names are code
// constants (never request-derived), so the generated SQL is injection-free even though it
// interpolates the name.
type colVal struct {
	name string
	val  any    // nil | string | int64 | float64 | bool | time.Time (pre-encoded for jsonb/array)
	cast string // "", "jsonb", "text[]"
}

// full projection column order — the single source of truth for SELECT and scanProvider.
const fullColumns = `id, display_name, category, description, logo_url, status,
	compliance_review_status, op_state, visibility, priority, base_url, api_version, auth_scheme,
	auth_header, auth_query_param, capabilities, region, docs_url, webhook_config, bulk_api,
	batch_api, retry_policy, timeout_ms, rate_limit_rpm, concurrency_limit, daily_limit,
	monthly_limit, breaker_threshold, breaker_cooldown_s, credit_sync, credits_remaining,
	unit_cost_credits, cost_currency, sla_uptime_pct, correlation_group, sunset_at,
	confidence_score, cost_score, performance_score, success_score, failure_score, health_score,
	avg_latency_ms, last_health_at, last_failure_at, last_success_at, last_sync_at, tags, notes,
	attrs, archived_at, created_at, updated_at, updated_by`

// catalog projection column order — matches the providers_catalog view (migration 0005).
const catalogColumns = `id, display_name, category, description, logo_url, status, capabilities,
	region, docs_url, tags, sunset_at, archived_at`

// scanProvider maps a full-projection row (fullColumns order) into a Provider.
func scanProvider(row []*string) Provider {
	p := Provider{
		ID:                     sstr(row[0]),
		DisplayName:            sstr(row[1]),
		Category:               sstr(row[2]),
		Description:            sstr(row[3]),
		LogoURL:                sstr(row[4]),
		Status:                 sstr(row[5]),
		ComplianceReviewStatus: sstr(row[6]),
		OpState:                sstr(row[7]),
		Visibility:             sstr(row[8]),
		Priority:               i64p(row[9]),
		BaseURL:                sstr(row[10]),
		APIVersion:             sstr(row[11]),
		AuthScheme:             sstr(row[12]),
		AuthHeader:             sstr(row[13]),
		AuthQueryParam:         sstr(row[14]),
		Capabilities:           parseCaps(row[15]),
		Region:                 parseTextArray(row[16]),
		DocsURL:                sstr(row[17]),
		WebhookConfig:          rawj(row[18]),
		BulkAPI:                boolp(row[19]),
		BatchAPI:               boolp(row[20]),
		RetryPolicy:            rawj(row[21]),
		TimeoutMS:              i64p(row[22]),
		RateLimitRPM:           i64p(row[23]),
		ConcurrencyLimit:       i64p(row[24]),
		DailyLimit:             i64p(row[25]),
		MonthlyLimit:           i64p(row[26]),
		BreakerThreshold:       i64p(row[27]),
		BreakerCooldownS:       i64p(row[28]),
		CreditSync:             rawj(row[29]),
		CreditsRemaining:       i64p(row[30]),
		UnitCostCredits:        i64p(row[31]),
		CostCurrency:           sstr(row[32]),
		SLAUptimePct:           f64p(row[33]),
		CorrelationGroup:       sstr(row[34]),
		SunsetAt:               tsp(row[35]),
		ConfidenceScore:        f64p(row[36]),
		CostScore:              f64p(row[37]),
		PerformanceScore:       f64p(row[38]),
		SuccessScore:           f64p(row[39]),
		FailureScore:           f64p(row[40]),
		HealthScore:            f64p(row[41]),
		AvgLatencyMS:           f64p(row[42]),
		LastHealthAt:           tsp(row[43]),
		LastFailureAt:          tsp(row[44]),
		LastSuccessAt:          tsp(row[45]),
		LastSyncAt:             tsp(row[46]),
		Tags:                   parseTextArray(row[47]),
		Notes:                  sstr(row[48]),
		Attrs:                  rawj(row[49]),
		ArchivedAt:             tsp(row[50]),
		CreatedAt:              tsp(row[51]),
		UpdatedAt:              tsp(row[52]),
		UpdatedBy:              sstr(row[53]),
	}
	return p
}

// scanCatalog maps a catalog-projection row (catalogColumns order) into a partial Provider.
func scanCatalog(row []*string) Provider {
	return Provider{
		ID:           sstr(row[0]),
		DisplayName:  sstr(row[1]),
		Category:     sstr(row[2]),
		Description:  sstr(row[3]),
		LogoURL:      sstr(row[4]),
		Status:       sstr(row[5]),
		Capabilities: parseCaps(row[6]),
		Region:       parseTextArray(row[7]),
		DocsURL:      sstr(row[8]),
		Tags:         parseTextArray(row[9]),
		SunsetAt:     tsp(row[10]),
		ArchivedAt:   tsp(row[11]),
		Visibility:   VisibilityTenantReadable, // by construction of the view
		// op_state is intentionally absent from the tenant projection.
	}
}

// --- column value helpers (nil-safe decode of the pg text protocol) ---

func sstr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64p(p *string) *int64 {
	if p == nil {
		return nil
	}
	v, err := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func f64p(p *string) *float64 {
	if p == nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(*p), 64)
	if err != nil {
		return nil
	}
	return &v
}

func boolp(p *string) *bool {
	if p == nil {
		return nil
	}
	b := *p == "t" || *p == "true"
	return &b
}

func tsp(p *string) *time.Time {
	if p == nil || *p == "" {
		return nil
	}
	t := parseTS(*p)
	if t.IsZero() {
		return nil
	}
	return &t
}

func rawj(p *string) json.RawMessage {
	if p == nil || *p == "" {
		return nil
	}
	return json.RawMessage(*p)
}

func parseCaps(p *string) []Capability {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	var caps []Capability
	if err := json.Unmarshal([]byte(*p), &caps); err != nil {
		return nil
	}
	return caps
}

// parseTS parses a Postgres timestamptz text rendering (or RFC3339) into a time.Time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseTextArray parses a Postgres text[] output literal like {a,b,"c d"} into a []string.
// Empty ({}) or NULL yields nil. Handles double-quoted elements with \" and \\ escapes.
func parseTextArray(p *string) []string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	esc := false
	flush := func() { out = append(out, cur.String()); cur.Reset() }
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			cur.WriteByte(ch)
			esc = false
		case ch == '\\':
			esc = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ',' && !inQuote:
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	// Unquoted NULL element sentinel — treat as empty string (arrays here never store NULLs).
	for i, v := range out {
		if v == "NULL" {
			out[i] = ""
		}
	}
	return out
}

// colsBuilder accumulates colVal assignments, skipping unset (nil / empty) values so a PATCH
// touches only supplied columns. Column names are code constants. Force* variants always emit
// (used for create-time defaults). Encoding mirrors the pg text protocol casts.
type colsBuilder struct{ cv []colVal }

func (b *colsBuilder) str(name, v string) {
	if v != "" {
		b.cv = append(b.cv, colVal{name, v, ""})
	}
}
func (b *colsBuilder) strForce(name, v string) { b.cv = append(b.cv, colVal{name, v, ""}) }
func (b *colsBuilder) i64(name string, v *int64) {
	if v != nil {
		b.cv = append(b.cv, colVal{name, *v, ""})
	}
}
func (b *colsBuilder) f64(name string, v *float64) {
	if v != nil {
		b.cv = append(b.cv, colVal{name, *v, ""})
	}
}
func (b *colsBuilder) boolean(name string, v *bool) {
	if v != nil {
		b.cv = append(b.cv, colVal{name, *v, ""})
	}
}
func (b *colsBuilder) ts(name string, v *time.Time) {
	if v != nil {
		b.cv = append(b.cv, colVal{name, *v, ""})
	}
}
func (b *colsBuilder) jsonb(name string, v json.RawMessage) {
	if len(v) > 0 {
		b.cv = append(b.cv, colVal{name, string(v), "jsonb"})
	}
}
func (b *colsBuilder) arr(name string, v []string) {
	if v != nil {
		b.cv = append(b.cv, colVal{name, formatTextArray(v), "text[]"})
	}
}
func (b *colsBuilder) caps(name string, v []Capability) {
	if v != nil {
		raw, _ := json.Marshal(v)
		b.cv = append(b.cv, colVal{name, string(raw), "jsonb"})
	}
}

// providerToInsertCols emits the config columns worth copying when duplicating a row (identity,
// integration descriptor, capabilities, limits, tags) — never runtime scores, credit balances,
// health timestamps, or archived_at, which a fresh draft must not inherit.
func providerToInsertCols(p Provider) []colVal {
	var b colsBuilder
	b.strForce("id", p.ID)
	b.strForce("display_name", p.DisplayName)
	b.str("category", p.Category)
	b.str("description", p.Description)
	b.str("logo_url", p.LogoURL)
	b.strForce("status", p.Status)
	b.str("compliance_review_status", p.ComplianceReviewStatus)
	b.strForce("op_state", p.OpState)
	b.strForce("visibility", p.Visibility)
	b.i64("priority", p.Priority)
	b.str("base_url", p.BaseURL)
	b.str("api_version", p.APIVersion)
	b.str("auth_scheme", p.AuthScheme)
	b.str("auth_header", p.AuthHeader)
	b.str("auth_query_param", p.AuthQueryParam)
	b.caps("capabilities", p.Capabilities)
	b.arr("region", p.Region)
	b.str("docs_url", p.DocsURL)
	b.jsonb("webhook_config", p.WebhookConfig)
	b.boolean("bulk_api", p.BulkAPI)
	b.boolean("batch_api", p.BatchAPI)
	b.jsonb("retry_policy", p.RetryPolicy)
	b.i64("timeout_ms", p.TimeoutMS)
	b.i64("rate_limit_rpm", p.RateLimitRPM)
	b.i64("concurrency_limit", p.ConcurrencyLimit)
	b.i64("daily_limit", p.DailyLimit)
	b.i64("monthly_limit", p.MonthlyLimit)
	b.i64("breaker_threshold", p.BreakerThreshold)
	b.i64("breaker_cooldown_s", p.BreakerCooldownS)
	b.jsonb("credit_sync", p.CreditSync)
	b.i64("unit_cost_credits", p.UnitCostCredits)
	b.str("cost_currency", p.CostCurrency)
	b.f64("sla_uptime_pct", p.SLAUptimePct)
	b.str("correlation_group", p.CorrelationGroup)
	b.ts("sunset_at", p.SunsetAt)
	b.arr("tags", p.Tags)
	b.str("notes", p.Notes)
	b.jsonb("attrs", p.Attrs)
	return b.cv
}

// formatTextArray renders elements as a Postgres array literal ({"a","b"}); nil/empty -> "{}".
// Every element is double-quoted with \" / \\ escaping so commas, braces, and spaces are safe.
func formatTextArray(elems []string) string {
	if len(elems) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(e))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
