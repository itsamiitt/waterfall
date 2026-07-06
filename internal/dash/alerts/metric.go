package alerts

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnknownMetric is returned when a rule's metric is outside the closed vocabulary (doc 10 §4).
// The HTTP layer maps it to 422 validation_failed.
var ErrUnknownMetric = errors.New("alerts: metric is not in the closed vocabulary")

// ErrInvalidScope is returned when a rule's scope carries a key not allowed for its metric.
var ErrInvalidScope = errors.New("alerts: scope key not allowed for this metric")

// ErrInvalidOp is returned for an op outside {gt,lt,gte,lte}.
var ErrInvalidOp = errors.New("alerts: op must be one of gt, lt, gte, lte")

// MetricDef is one entry of the CLOSED alert-rule metric vocabulary (doc 10 §4). The evaluator
// switches over Metric; the SPA enum mirrors this list via GET /v1/admin/meta/enums (P7). Adding an
// entry requires a change here, the evaluator switch, and the UI enum in lockstep.
type MetricDef struct {
	Metric      string   `json:"metric"`
	Source      string   `json:"source"`
	Unit        string   `json:"unit"`
	ScopeKeys   []string `json:"scope_keys"`
	PointInTime bool     `json:"point_in_time"`
}

// Metrics is the CLOSED alert-rule metric vocabulary (doc 10 §4: 17 entries incl. cost.anomaly, the
// anomaly deliverable of doc 12 §P6 — trailing 28d same-day-of-week median, dual threshold). This
// slice, the evaluator switch (metriceval.go), the /meta/enums served list (overview/enums.go), and
// the doc 10 §4 table are kept in lockstep (OI-P6-1); the parity tests below and in overview pin it.
var Metrics = []MetricDef{
	{"provider.success_rate", "provider_stats_1m", "ratio", []string{"provider_id"}, false},
	{"provider.error_rate", "provider_stats_1m", "ratio", []string{"provider_id"}, false},
	{"provider.p95_latency_ms", "provider_stats_1m", "ms", []string{"provider_id"}, false},
	{"provider.credits_remaining", "providers", "credits", []string{"provider_id"}, true},
	{"key.credits_remaining", "provider_keys", "credits", []string{"provider_id", "pool_id"}, true},
	{"key.consecutive_failures", "provider_keys", "count", []string{"provider_id", "pool_id"}, true},
	{"key.active_ratio_in_pool", "provider_keys", "ratio", []string{"pool_id"}, true},
	{"queue.depth", "queue_stats_1m", "jobs", []string{"queue"}, false},
	{"queue.oldest_age_s", "queue_stats_1m", "seconds", []string{"queue"}, false},
	{"queue.dead_count", "queue_stats_1m", "jobs", []string{"queue"}, false},
	{"worker.lost_count", "workers", "workers", []string{"kind", "queue"}, true},
	{"worker.heartbeat_age_s", "workers", "seconds", []string{"kind", "queue"}, true},
	{"cost.daily_credits", "cost_rollup_1d", "credits", []string{"provider_id", "workflow_key"}, true},
	{"cost.budget_burn_pct", "cost_rollup_1d", "percent", []string{"scope", "scope_key", "period"}, true},
	{"cost.anomaly", "cost_rollup_1d", "credits", []string{"provider_id", "workflow_key"}, true},
	{"system.sse_clients", "self_monitor", "clients", nil, true},
	{"system.aggregator_lag_s", "self_monitor", "seconds", nil, true},
}

var metricByName = func() map[string]MetricDef {
	m := make(map[string]MetricDef, len(Metrics))
	for _, d := range Metrics {
		m[d.Metric] = d
	}
	return m
}()

// metricDef looks up a metric definition; ok=false for an unknown metric.
func metricDef(metric string) (MetricDef, bool) {
	d, ok := metricByName[metric]
	return d, ok
}

var validOps = map[string]bool{"gt": true, "lt": true, "gte": true, "lte": true}

// validateRule checks metric membership, op, and scope keys against the closed vocabulary.
func validateRule(r Rule) error {
	def, ok := metricByName[r.Metric]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMetric, r.Metric)
	}
	if !validOps[r.Op] {
		return fmt.Errorf("%w: %q", ErrInvalidOp, r.Op)
	}
	for k := range r.Scope {
		if !contains(def.ScopeKeys, k) {
			return fmt.Errorf("%w: %q not in %v for %s", ErrInvalidScope, k, def.ScopeKeys, r.Metric)
		}
	}
	return nil
}

// metricReadsPlatform reports whether a metric's SOURCE rollup is Class-P (read under a
// platform-bound tx: provider/key/queue/worker/system stats have no tenant_id and their RLS admits
// only app_current_tenant()='platform'). cost.* sources are Class-T and read under the owning
// Tenant's tx. The episode + notification WRITES are always Class-T (owning Tenant) regardless.
func metricReadsPlatform(metric string) bool {
	return !strings.HasPrefix(metric, "cost.")
}

// compare applies the rule operator to a computed value vs its threshold.
func compare(op string, value, threshold float64) bool {
	switch op {
	case "gt":
		return value > threshold
	case "lt":
		return value < threshold
	case "gte":
		return value >= threshold
	case "lte":
		return value <= threshold
	}
	return false
}

// canonicalScope renders a rule scope as a deterministic string for the episode dedupe key
// (sha256(tenant || rule || canonical scope-instance), doc 03 §2.4). Keys are sorted so the same
// scope always canonicalises identically.
func canonicalScope(scope map[string]string) string {
	if len(scope) == 0 {
		return ""
	}
	keys := make([]string, 0, len(scope))
	for k := range scope {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(scope[k])
	}
	return b.String()
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
