package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Auditor is the consumer-side view of the per-tenant hash-chained audit log (satisfied by
// *audit.Log). Every mutating service method appends one entry with string-only before/after
// snapshots so the chain re-canonicalizes identically after a jsonb round-trip.
type Auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

var _ Auditor = (*audit.Log)(nil)

const (
	defaultProbeTimeout = 5 * time.Second
	benchmarkSamples    = 3
	defaultPoolName     = "default"
)

// Service is the Provider Management business layer over a Store + Auditor. Reads are role-aware
// (operator => full row via PlatformTx; tenant => catalog projection). Probe actions
// (test/health-check/benchmark) reuse provider.Call for G3-bounded execution with a key resolved
// through the egress KeyResolver seam.
type Service struct {
	store    Store
	audit    Auditor
	resolver provider.KeyResolver // optional; nil => probe actions report a typed no-key result
	now      func() time.Time
}

// newService is the low-level constructor (used by tests with fakes).
func newService(store Store, aud Auditor, resolver provider.KeyResolver, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, audit: aud, resolver: resolver, now: now}
}

// --- reads (role-aware) ---

// List returns providers for the caller's scope, cursor-paginated and bounded. Operators see the
// full row; tenants see the tenant_readable catalog projection.
func (s *Service) List(ctx context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	if isOperator(ctx) {
		return s.store.ListFull(ctx, f, cur, limit)
	}
	return s.store.ListCatalog(ctx, f, cur, limit)
}

// Get returns one provider for the caller's scope (ErrNotFound never discloses cross-scope rows).
func (s *Service) Get(ctx context.Context, id string) (Provider, error) {
	if isOperator(ctx) {
		return s.store.GetFull(ctx, id)
	}
	return s.store.GetCatalog(ctx, id)
}

// GetMany returns the full rows for ids (operator scope; used by compare).
func (s *Service) GetMany(ctx context.Context, ids []string) ([]Provider, error) {
	return s.store.GetManyFull(ctx, ids)
}

// catalog returns the whole catalog for aggregation (rankings/coverage), role-aware and bounded
// to the 200 hard cap.
func (s *Service) catalog(ctx context.Context, f Filter) ([]Provider, error) {
	var out []Provider
	cur := db.Cursor{}
	for {
		page, next, err := s.List(ctx, f, cur, 200)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next.ID == "" || len(page) == 0 || len(out) >= 2000 {
			break
		}
		cur = next
	}
	return out, nil
}

// --- writes (operator scope; each audited) ---

// Create inserts a new provider from the prepared column set and returns the stored row.
func (s *Service) Create(ctx context.Context, cols []colVal) (Provider, error) {
	p, err := s.store.Insert(ctx, cols)
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_create", p.ID, nil, snap(p)); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// Patch applies a partial update and returns the updated row, auditing before/after.
func (s *Service) Patch(ctx context.Context, id string, cols []colVal) (Provider, error) {
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	after, err := s.store.Update(ctx, id, cols)
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_update", id, snap(before), snap(after)); err != nil {
		return Provider{}, err
	}
	return after, nil
}

// SetOpState applies a lifecycle op_state action (enable/disable/pause/maintenance) through the
// valid-transition guard, auditing before/after. ErrInvalidTransition when the move is illegal.
func (s *Service) SetOpState(ctx context.Context, id, action, reason string) (Provider, error) {
	target, ok := opStateAction[action]
	if !ok {
		return Provider{}, ErrValidation
	}
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if !canTransition(before.OpState, target) {
		return Provider{}, ErrInvalidTransition
	}
	after, err := s.store.Update(ctx, id, []colVal{{name: "op_state", val: target}})
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_"+action, id, snap(before), snapReason(after, reason)); err != nil {
		return Provider{}, err
	}
	return after, nil
}

// Archive soft-deletes a provider (sets archived_at; history intact). Idempotent-safe: archiving
// an already-archived provider re-stamps archived_at.
func (s *Service) Archive(ctx context.Context, id string) (Provider, error) {
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	after, err := s.store.Update(ctx, id, []colVal{{name: "archived_at", val: s.now().UTC()}})
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_archive", id, snap(before), snap(after)); err != nil {
		return Provider{}, err
	}
	return after, nil
}

// Delete hard-removes a provider, auditing the pre-image. Returns ErrNotFound when absent. The
// approval gate for this destructive action slots in at the handler once P4 lands (OI-IP-2).
func (s *Service) Delete(ctx context.Context, id string) error {
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return err
	}
	deleted, err := s.store.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return s.writeAudit(ctx, "provider_delete", id, snap(before), nil)
}

// Duplicate clones a provider's config into a fresh draft (new slug, status reset to
// DEPRIORITIZED/disabled, no inherited scores or credit balances).
func (s *Service) Duplicate(ctx context.Context, srcID, newID, newName string) (Provider, error) {
	src, err := s.store.GetFull(ctx, srcID)
	if err != nil {
		return Provider{}, err
	}
	src.ID = newID
	if newName != "" {
		src.DisplayName = newName
	}
	src.Status = StatusDeprioritized
	src.ComplianceReviewStatus = "pending"
	src.OpState = OpDisabled
	if src.Visibility == "" {
		src.Visibility = VisibilityTenantReadable
	}
	p, err := s.store.Insert(ctx, providerToInsertCols(src))
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_duplicate", p.ID, snap(src), snap(p)); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// RefreshMetadata is a P1 stub (doc 12 M2): it records last_sync_at so the "metadata refreshed"
// signal is durable. Re-pulling descriptor metadata from the vendor lands with the health module.
func (s *Service) RefreshMetadata(ctx context.Context, id string) (Provider, error) {
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	after, err := s.store.Update(ctx, id, []colVal{{name: "last_sync_at", val: s.now().UTC()}})
	if err != nil {
		return Provider{}, err
	}
	if err := s.writeAudit(ctx, "provider_refresh_metadata", id, snap(before), snap(after)); err != nil {
		return Provider{}, err
	}
	return after, nil
}

// SyncCredits records a provider-reported credit balance. The manual path (an operator-supplied
// value) is fully implemented; the endpoint path (credit_sync.mode='endpoint') performs a bounded
// probe and records last_sync_at only — parsing a vendor-specific balance body lands with the
// health/credit-sync worker (doc 10). Either path stamps last_sync_at.
func (s *Service) SyncCredits(ctx context.Context, id string, manual *int64) (Provider, ProbeResult, error) {
	before, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, ProbeResult{}, err
	}
	cols := []colVal{{name: "last_sync_at", val: s.now().UTC()}}
	probe := ProbeResult{OK: true, Reason: "manual"}
	if manual != nil {
		cols = append(cols, colVal{name: "credits_remaining", val: *manual})
	} else if mode := creditSyncMode(before.CreditSync); mode == "endpoint" {
		probe = s.probe(ctx, before, 1)
		probe.Reason = "endpoint_probe_only"
	} else {
		probe.Reason = "noop"
	}
	after, err := s.store.Update(ctx, id, cols)
	if err != nil {
		return Provider{}, ProbeResult{}, err
	}
	if err := s.writeAudit(ctx, "provider_sync_credits", id, snap(before), snap(after)); err != nil {
		return Provider{}, ProbeResult{}, err
	}
	return after, probe, nil
}

// Test runs one G3-bounded smoke probe through the real adapter and a leased key. It does NOT
// mutate the row (a pure smoke test). A missing key yields a typed result, never a crash.
func (s *Service) Test(ctx context.Context, id string) (ProbeResult, error) {
	p, err := s.store.GetFull(ctx, id)
	if err != nil {
		return ProbeResult{}, err
	}
	res := s.probe(ctx, p, 1)
	if err := s.writeAudit(ctx, "provider_test", id, nil, snap(p)); err != nil {
		return ProbeResult{}, err
	}
	return res, nil
}

// HealthCheck runs one probe and records last_health_at plus last_success_at / last_failure_at.
func (s *Service) HealthCheck(ctx context.Context, id string) (Provider, ProbeResult, error) {
	p, err := s.store.GetFull(ctx, id)
	if err != nil {
		return Provider{}, ProbeResult{}, err
	}
	res := s.probe(ctx, p, 1)
	now := s.now().UTC()
	cols := []colVal{{name: "last_health_at", val: now}}
	if res.OK {
		cols = append(cols, colVal{name: "last_success_at", val: now})
	} else {
		cols = append(cols, colVal{name: "last_failure_at", val: now})
	}
	after, err := s.store.Update(ctx, id, cols)
	if err != nil {
		return Provider{}, ProbeResult{}, err
	}
	if err := s.writeAudit(ctx, "provider_health_check", id, snap(p), snap(after)); err != nil {
		return Provider{}, ProbeResult{}, err
	}
	return after, res, nil
}

// Benchmark runs a small fixed sample of timed probes and summarizes latency/success. G3-bounded
// (each probe goes through provider.Call); G4 is respected because each probe spends at most one
// call's worth of credits.
func (s *Service) Benchmark(ctx context.Context, id string) (BenchmarkResult, error) {
	p, err := s.store.GetFull(ctx, id)
	if err != nil {
		return BenchmarkResult{}, err
	}
	var res BenchmarkResult
	res.Samples = benchmarkSamples
	var total, min, max int64
	for i := 0; i < benchmarkSamples; i++ {
		pr := s.probe(ctx, p, 1)
		if pr.OK {
			res.Successes++
		} else {
			res.Failures++
		}
		total += pr.LatencyMS
		if i == 0 || pr.LatencyMS < min {
			min = pr.LatencyMS
		}
		if pr.LatencyMS > max {
			max = pr.LatencyMS
		}
	}
	res.MinLatencyMS, res.MaxLatencyMS = min, max
	if benchmarkSamples > 0 {
		res.AvgLatencyMS = float64(total) / float64(benchmarkSamples)
	}
	if err := s.writeAudit(ctx, "provider_benchmark", id, nil, snap(p)); err != nil {
		return BenchmarkResult{}, err
	}
	return res, nil
}

// Compare returns side-by-side declared/measured rows for the given ids (operator scope).
func (s *Service) Compare(ctx context.Context, ids []string) ([]CompareEntry, error) {
	rows, err := s.store.GetManyFull(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Preserve request order.
	byID := make(map[string]Provider, len(rows))
	for _, p := range rows {
		byID[p.ID] = p
	}
	ordered := make([]Provider, 0, len(ids))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			ordered = append(ordered, p)
		}
	}
	return compareEntries(ordered), nil
}

// Rankings ranks the whole catalog by the named metric (operator scope).
func (s *Service) Rankings(ctx context.Context, metric string) ([]Ranking, error) {
	all, err := s.catalog(ctx, Filter{})
	if err != nil {
		return nil, err
	}
	return rankBy(all, metric), nil
}

// Coverage aggregates declared capabilities over the catalog (role-aware; TU+ via projection).
func (s *Service) Coverage(ctx context.Context) (CoverageReport, error) {
	all, err := s.catalog(ctx, Filter{})
	if err != nil {
		return CoverageReport{}, err
	}
	return coverage(all), nil
}

// --- probe execution (reuses provider.Call for G3-bounded execution) ---

// ProbeResult is one bounded probe outcome. It never carries secret material.
type ProbeResult struct {
	OK        bool   `json:"ok"`
	Reason    string `json:"reason,omitempty"`
	Class     string `json:"error_class,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Attempts  int    `json:"attempts"`
}

// BenchmarkResult summarizes a benchmark run.
type BenchmarkResult struct {
	Samples      int     `json:"samples"`
	Successes    int     `json:"successes"`
	Failures     int     `json:"failures"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	MinLatencyMS int64   `json:"min_latency_ms"`
	MaxLatencyMS int64   `json:"max_latency_ms"`
}

// probe builds an HTTPAdapter from the provider row and runs a single G3-bounded health call via
// provider.Call with a key resolved through the egress seam. maxAttempts bounds retries. It
// returns a typed result for the no-base-url / no-key cases rather than erroring or panicking.
func (s *Service) probe(ctx context.Context, p Provider, maxAttempts int) ProbeResult {
	if p.BaseURL == "" {
		return ProbeResult{OK: false, Reason: "no_base_url"}
	}
	needKey := p.AuthScheme != ""
	selector := ""
	if needKey {
		if s.resolver == nil {
			return ProbeResult{OK: false, Reason: "no_key_resolver"}
		}
		selector = p.ID + ":" + defaultPoolName
		if _, err := s.resolver.Resolve(selector); err != nil {
			return ProbeResult{OK: false, Reason: "no_key_available"}
		}
	}

	timeout := defaultProbeTimeout
	if p.TimeoutMS != nil && *p.TimeoutMS > 0 {
		timeout = time.Duration(*p.TimeoutMS) * time.Millisecond
	}
	client := &http.Client{
		Timeout:   timeout + time.Second,
		Transport: provider.NewAuthInjector(nil, s.resolver),
	}
	adapter := &provider.HTTPAdapter{
		NameV:   p.ID,
		BaseURL: p.BaseURL,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthScheme(p.AuthScheme),
			HeaderName:      p.AuthHeader,
			QueryParam:      p.AuthQueryParam,
			KeyPoolSelector: selector,
		},
		Caps:   toProviderCaps(p.Capabilities),
		Decode: func([]byte) (provider.Result, error) { return provider.Result{}, nil },
	}
	br := provider.NewBreaker(breakerThreshold(p), breakerCooldown(p), nil)
	pol := provider.CallPolicy{Timeout: timeout, MaxAttempts: maxAttempts, Backoff: 50 * time.Millisecond, MaxBackoff: time.Second}

	attempts := 0
	start := s.now()
	_, err := provider.Call(ctx, adapter, provider.Request{}, pol, br, &attempts)
	lat := s.now().Sub(start).Milliseconds()
	res := ProbeResult{OK: err == nil, LatencyMS: lat, Attempts: attempts}
	if err != nil {
		res.Class = domain.ClassOf(err).String()
		res.Reason = "call_failed"
	}
	return res
}

// --- audit + snapshot helpers ---

// writeAudit appends one hash-chained row under the caller's Principal. The tenant is read from
// ctx by audit.Append; the actor is the ctx Principal (operator for provider writes).
func (s *Service) writeAudit(ctx context.Context, action, id string, before, after json.RawMessage) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err
	}
	return s.audit.Append(ctx, audit.Entry{
		Action:      action,
		ObjectKind:  "providers",
		ObjectID:    id,
		ActorUserID: p.UserID,
		ActorRole:   db.RoleFromPrincipal(p),
		Before:      before,
		After:       after,
	})
}

// snap is a string-only pre/post-image (id, status, op_state) so the audit hash chain
// re-canonicalizes identically after a jsonb round-trip (no floats, per doc 05 §8.1).
func snap(p Provider) json.RawMessage {
	return jraw(map[string]string{"id": p.ID, "status": p.Status, "op_state": p.OpState})
}

func snapReason(p Provider, reason string) json.RawMessage {
	m := map[string]string{"id": p.ID, "status": p.Status, "op_state": p.OpState}
	if reason != "" {
		m["reason"] = reason
	}
	return jraw(m)
}

func jraw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// --- small helpers ---

func isOperator(ctx context.Context) bool {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return false
	}
	return db.RoleFromPrincipal(p) == "operator"
}

func toProviderCaps(caps []Capability) []provider.Capability {
	out := make([]provider.Capability, 0, len(caps))
	for _, c := range caps {
		out = append(out, provider.Capability{
			Field:              domain.Field(c.Field),
			Cost:               domain.Credits(c.CostCredits),
			ExpectedConfidence: domain.Confidence(c.ExpectedConfidence),
		})
	}
	return out
}

func breakerThreshold(p Provider) int {
	if p.BreakerThreshold != nil && *p.BreakerThreshold > 0 {
		return int(*p.BreakerThreshold)
	}
	return 5
}

func breakerCooldown(p Provider) time.Duration {
	if p.BreakerCooldownS != nil && *p.BreakerCooldownS > 0 {
		return time.Duration(*p.BreakerCooldownS) * time.Second
	}
	return 60 * time.Second
}

// creditSyncMode extracts {"mode":...} from the credit_sync jsonb, or "" when absent/malformed.
func creditSyncMode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var cfg struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ""
	}
	return cfg.Mode
}
