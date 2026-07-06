// features/health — fleet summary (doc 09 §5.1), worst-first. Screen → endpoint:
// GET /health/providers?status=&region=; POST /health/checks/run (Run checks now);
// GET/PUT /health/schedules (schedule editor). Row → /health/:providerId timeline.
import { useState } from "react";
import { Link } from "react-router";
import { useSseTopics } from "../../api/sse";
import { Button, EmptyState, Input, Select, type SelectOption } from "../../design/primitives";
import { Badge } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatLatencyMs } from "../../lib/format";
import { unknownStatus, type StatusDescriptor } from "../../lib/status";
import { toast } from "../../app/toast";
import { useFleetHealth, usePutSchedules, useRunChecks, useSchedules } from "./api";
import type { HealthFilter, HealthRow } from "./types";

const HEALTH: Record<string, StatusDescriptor> = {
  ok: { token: "ok", icon: "check", label: "ok" },
  up: { token: "ok", icon: "check", label: "ok" },
  degraded: { token: "warn", icon: "triangle", label: "degraded" },
  degrade: { token: "warn", icon: "triangle", label: "degraded" },
  down: { token: "error", icon: "x", label: "down" },
};
const healthDesc = (h: string): StatusDescriptor => HEALTH[h] ?? unknownStatus(h);

const regionOpts: SelectOption[] = [
  { value: "us", label: "us" },
  { value: "eu", label: "eu" },
  { value: "apac", label: "apac" },
];

function UptimePct({ pct }: { pct?: number }) {
  if (pct === undefined) return <span>—</span>;
  return (
    <span className="uptime-mini" title={`${pct}% (90d)`}>
      <span className="credits-bar" aria-hidden="true"><span style={{ width: `${Math.max(0, Math.min(100, pct))}%` }} /></span>
      {" "}{pct}%
    </span>
  );
}

function ScheduleEditor() {
  const q = useSchedules();
  const put = usePutSchedules();
  if (q.isPending || !q.data) return null;
  const s = q.data.schedules[0];
  if (!s) return null;
  return (
    <ScheduleForm
      interval={s.interval_s}
      jitter={s.jitter_pct}
      regions={s.regions.join(",")}
      busy={put.isPending}
      onSave={(interval, jitter, regions) =>
        put.mutate(
          { schedules: q.data!.schedules.map((row, i) => (i === 0 ? { ...row, interval_s: interval, jitter_pct: jitter, regions } : row)) },
          { onSuccess: () => toast.success("Schedules updated") },
        )
      }
    />
  );
}

function ScheduleForm({
  interval, jitter, regions, busy, onSave,
}: { interval: number; jitter: number; regions: string; busy: boolean; onSave: (i: number, j: number, r: string[]) => void }) {
  const [iv, setIv] = useState(String(interval));
  const [jt, setJt] = useState(String(jitter));
  const [rg, setRg] = useState(regions);
  return (
    <div className="section">
      <div className="section-title">Check schedule (PUT /health/schedules)</div>
      <div className="filter-bar">
        <Input label="Interval (s)" value={iv} onChange={setIv} inputMode="numeric" />
        <Input label="Jitter (%)" value={jt} onChange={setJt} inputMode="numeric" />
        <Input label="Regions (csv)" value={rg} onChange={setRg} />
        <Button size="sm" variant="primary" loading={busy}
          onClick={() => onSave(Number(iv) || 0, Number(jt) || 0, rg.split(",").map((x) => x.trim()).filter(Boolean))}>
          Save
        </Button>
      </div>
    </div>
  );
}

export default function FleetHealth() {
  useSseTopics(["provider"]);
  const [filter, setFilter] = useState<HealthFilter>({});
  const q = useFleetHealth(filter);
  const runChecks = useRunChecks();
  const rows: HealthRow[] = q.data ?? [];

  function runAll() {
    const ids = rows.map((r) => r.provider_id);
    runChecks.mutate(ids, {
      onSuccess: (res) => {
        if (res && typeof res === "object" && "job_id" in res) toast.success(`Checks queued (job ${res.job_id.slice(0, 8)})`);
        else toast.success("Checks run");
      },
    });
  }

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Provider health</h1>
      </div>

      <div className="action-bar">
        <Button size="sm" loading={runChecks.isPending} disabled={rows.length === 0} onClick={runAll}>
          Run checks now
        </Button>
        <Select label="Region" options={regionOpts} value={filter.region ?? ""} placeholder="all"
          onChange={(v) => setFilter((f) => ({ ...f, region: v || undefined }))} />
      </div>

      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load fleet health"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.isPending ? (
        <div className="skeleton" style={{ height: 320 }} aria-busy="true" />
      ) : rows.length === 0 ? (
        <EmptyState variant="zero-data" title="No providers to check" action={{ label: "Go to providers", href: "/providers" }} />
      ) : (
        <table className="p-table">
          <thead>
            <tr>
              <th scope="col">Provider</th><th scope="col">Health</th><th scope="col">Uptime 90d</th>
              <th scope="col">P95</th><th scope="col">P99</th><th scope="col">Last error</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const d = healthDesc(r.health);
              return (
                <tr key={r.provider_id}>
                  <td><Link to={`/health/${encodeURIComponent(r.provider_id)}`}>{r.display_name ?? r.provider_id}</Link></td>
                  <td><Badge status={d.token} label={d.label} icon={d.icon} /></td>
                  <td><UptimePct pct={r.uptime_90d_pct} /></td>
                  <td>{formatLatencyMs(r.p95_ms)}</td>
                  <td>{formatLatencyMs(r.p99_ms)}</td>
                  <td>{r.last_error_class ?? "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      <ScheduleEditor />
    </>
  );
}
