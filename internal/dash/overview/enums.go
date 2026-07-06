package overview

import (
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/alerts"
	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/realtime"
	"github.com/enrichment/waterfall/internal/dash/rotation"
)

// enums is GET /v1/admin/meta/enums (doc 04 §2.13): the closed vocabularies, served from the
// SAME code constants the handlers enforce, so the UI enum parity test (doc 10 OBS-2:
// vocab ⊆ handler ⊆ UI) can diff against a live instance. Every list here references its
// owning package's exported constant — never a re-typed copy — except the five-value
// config-epoch kind enum, whose three sentinel kinds have no lifecycle constants (they are
// epoch channels, not configver-versioned kinds; migration 0006 CHECK is their authority).
func (h *handlers) enums(w http.ResponseWriter, r *http.Request) {
	strategies := make([]string, 0, len(rotation.Strategies))
	for _, s := range rotation.Strategies {
		strategies = append(strategies, s.Name)
	}
	alertMetrics := make([]map[string]any, 0, len(alerts.Metrics))
	for _, m := range alerts.Metrics {
		scope := m.ScopeKeys
		if scope == nil {
			scope = []string{}
		}
		alertMetrics = append(alertMetrics, map[string]any{
			"metric": m.Metric, "source": m.Source, "unit": m.Unit,
			"scope_keys": scope, "point_in_time": m.PointInTime,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		// Providers (migration 0005 CHECKs; doc 04 §2.3).
		"provider_statuses":  []string{"ACTIVE-CANDIDATE", "DEPRIORITIZED", "EXCLUDED"},
		"provider_op_states": []string{"enabled", "disabled", "paused", "maintenance"},
		// Provider Keys (KM-3 vocabulary, internal/dash/keys).
		"key_statuses": []string{
			keys.StatusActive, keys.StatusDisabled, keys.StatusPaused, keys.StatusExhausted,
			keys.StatusRateLimited, keys.StatusAuthFailed, keys.StatusExpired,
			keys.StatusRotating, keys.StatusArchived,
		},
		// Key Pool strategies (rotation catalog, doc 07 §8).
		"pool_strategies": strategies,
		// Config versioning (internal/dash/configver; epoch kinds per migration 0006 CHECK).
		"config_kinds": []string{
			configver.KindRoutingPolicy, configver.KindWaterfallWorkflow,
			"alert_ruleset", "provider_catalog", "key_pool",
		},
		"config_statuses": []string{
			configver.StatusDraft, configver.StatusValidated,
			configver.StatusPublished, configver.StatusArchived,
		},
		// Workers (migration 0008 CHECKs; doc 06 §4).
		"worker_states":         []string{"starting", "running", "draining", "paused", "stopped", "lost"},
		"worker_desired_states": []string{"running", "draining", "paused", "stopped"},
		// Approvals (internal/dash/approvals).
		"approval_states": []string{
			approvals.StatusPending, approvals.StatusApproved, approvals.StatusRejected,
			approvals.StatusExpired, approvals.StatusCancelled, approvals.StatusExecuted,
			approvals.StatusFailed,
		},
		"approval_action_kinds": approvals.AllActionKinds,
		// Alerts (internal/dash/alerts.Metrics — 17 incl. cost.anomaly, doc 10 §4 / OI-P6-1).
		"alert_metrics":       alertMetrics,
		"alert_channel_types": []string{"email", "slack", "teams", "discord", "webhook"},
		// SSE (internal/dash/realtime; doc 04 §3.2).
		"sse_topics":      realtime.Topics,
		"sse_event_names": realtime.EventNames,
		// Error codes (doc 04 §1.6 registry — closed).
		"error_codes": []string{
			"invalid_json", "missing_idempotency_key", "invalid_cursor", "invalid_filter",
			"window_out_of_range", "payload_too_large", "unauthorized", "mfa_required",
			"forbidden", "csrf_required", "csrf_invalid", "ip_not_allowed", "not_found",
			"idempotency_key_reuse", "conflict", "version_conflict", "approval_required",
			"bulk_job_conflict", "validation_failed", "rate_limited", "queue_full",
			"internal", "egress_blocked", "sse_saturated",
		},
	})
}
