// features/health — provider timeline panel (doc 09 §5.1), reused by /health/:providerId AND the
// Provider detail health tab (doc 09 §2.2 "same component"). Screen → endpoint:
// GET /health/providers/{id}/timeline (90-day strip + 48h heatmap) + GET /providers/{id}/stats
// (P95/P99 overlay). Live via the `provider` SSE topic (provider.health.changed invalidates).
import { useState } from "react";
import { useSseTopics } from "../../api/sse";
import { EmptyState, TimeRangePicker, type TimePreset, type TimeRange } from "../../design/primitives";
import { TimeSeries } from "../../lib/charts";
import { isApiError } from "../../api/client";
import { formatLatencyMs, formatPercent } from "../../lib/format";
import { useProviderStats } from "../providers/api";
import { useTimeline } from "./api";
import { UptimeBar } from "./UptimeBar";
import { overallUptimePct, segmentStyle, type HealthState, type HourBucket } from "./timelineModel";

const RETENTION_90D_S = 90 * 24 * 60 * 60;
const TOKEN_VAR: Record<string, string> = {
  ok: "var(--status-ok)",
  warn: "var(--status-warn)",
  error: "var(--status-error)",
  neutral: "var(--color-bg-sunken)",
};

function hourState(h: HourBucket): HealthState {
  if (h.status) return h.status;
  if (h.success_rate === undefined) return "no_data";
  if (h.success_rate >= 0.99) return "up";
  if (h.success_rate >= 0.9) return "degraded";
  return "down";
}

function HourHeatmap({ hours }: { hours: readonly HourBucket[] }) {
  if (hours.length === 0) return <p className="p-field-description">No hourly buckets yet.</p>;
  return (
    <div className="hour-heatmap" role="img" aria-label="Success rate by hour, last 48 hours">
      {hours.map((h, i) => {
        const st = segmentStyle(hourState(h));
        return (
          <span
            key={i}
            className="hour-cell"
            style={{ background: st.noData ? TOKEN_VAR.neutral : TOKEN_VAR[st.token] }}
            title={`${h.ts} · ${st.label}${h.success_rate !== undefined ? ` · ${formatPercent(h.success_rate)}` : ""}`}
          />
        );
      })}
    </div>
  );
}

function initialRange(): TimeRange & { preset: TimePreset | "custom" } {
  const to = new Date();
  const from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
  const iso = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, "Z");
  return { from: iso(from), to: iso(to), res: "1h", preset: "24h" };
}

export function ProviderTimelinePanel({ providerId }: { providerId: string }) {
  useSseTopics(["provider"]);
  const [range, setRange] = useState(initialRange);
  const q = useTimeline(providerId);
  const stats = useProviderStats(providerId, range);

  if (q.isError) {
    return (
      <EmptyState
        variant="error"
        title="Could not load the timeline"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }}
      />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 120 }} aria-busy="true" aria-label="Loading timeline" />;

  const uptime = overallUptimePct(q.data.days);
  return (
    <div className="section">
      <div className="section-title">
        90-day uptime {uptime !== null ? <span className="page-header-meta">{uptime}%</span> : null}
      </div>
      <UptimeBar days={q.data.days} />

      <div className="section-title">Success rate by hour (48h)</div>
      <HourHeatmap hours={q.data.hours} />

      <div className="section-title">Latency P95 / P99</div>
      <TimeRangePicker value={range} onChange={setRange} presets={["24h", "7d", "30d"]} maxWindowS={RETENTION_90D_S} />
      {stats.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" />
      ) : (stats.data?.points.length ?? 0) === 0 ? (
        <EmptyState variant="zero-data" title="No latency samples in this window" />
      ) : (
        <TimeSeries
          data={stats.data!.points}
          series={[
            { key: "p95_ms", label: "p95" },
            { key: "p99_ms", label: "p99" },
          ]}
          yTickFormatter={(v) => formatLatencyMs(v)}
        />
      )}
    </div>
  );
}
