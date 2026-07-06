// features/providers — stats tab (doc 09 §2.2). Screen → endpoint:
// GET /providers/{id}/stats?res=&from=&to= (per-error-class failure series + P95/P99). The
// TimeRangePicker is bounded by rollup retention (doc 04 §1.8); recharts arrives via the lazy
// feature chunk (lib/charts is never in the initial bundle — doc 08 §10).
import { useState } from "react";
import { EmptyState, TimeRangePicker, type TimeRange, type TimePreset } from "../../design/primitives";
import { TimeSeries } from "../../lib/charts";
import { isApiError } from "../../api/client";
import { formatLatencyMs } from "../../lib/format";
import { useProviderStats } from "./api";

const RETENTION_2Y_S = 2 * 365 * 24 * 60 * 60;

function initialRange(): TimeRange & { preset: TimePreset | "custom" } {
  const to = new Date();
  const from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
  const iso = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, "Z");
  return { from: iso(from), to: iso(to), res: "1h", preset: "24h" };
}

export function StatsTab({ id }: { id: string }) {
  const [range, setRange] = useState(initialRange);
  const q = useProviderStats(id, range);
  const points = q.data?.points ?? [];

  return (
    <div className="section">
      <TimeRangePicker
        value={range}
        onChange={setRange}
        presets={["24h", "7d", "30d"]}
        maxWindowS={RETENTION_2Y_S}
      />
      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load stats"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={isApiError(q.error) ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" />
      ) : points.length === 0 ? (
        <EmptyState variant="zero-data" title="No stats in this window yet" />
      ) : (
        <>
          <div className="section-title">Latency (P95 / P99)</div>
          <TimeSeries
            data={points}
            series={[
              { key: "p95_ms", label: "p95" },
              { key: "p99_ms", label: "p99" },
            ]}
            yTickFormatter={(v) => formatLatencyMs(v)}
          />
          <div className="section-title">Requests vs failures</div>
          <TimeSeries
            data={points}
            series={[
              { key: "successes", label: "successes" },
              { key: "failures", label: "failures" },
            ]}
          />
        </>
      )}
    </div>
  );
}
