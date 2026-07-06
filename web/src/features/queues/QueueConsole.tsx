// features/queues/QueueConsole.tsx — /queues/:name. Enq/deq sparklines, the oldest-age lead
// StatTile, the state-filtered Enrichment Job table (each count deep-links ?state=), and the
// worker-intent control with the honest "actuation is deploy-layer" note (doc 09 §8, doc 04 §2.8).
import { Suspense, lazy, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { isApiError } from "../../api/client";
import { Badge, Button, EmptyState, Input, StatTile, Table, type ColumnDef } from "../../design/primitives";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { errorClassInfo, type ErrorClass, ERROR_CLASSES } from "../../lib/status";
import { formatCount, formatDurationS, relativeTime } from "../../lib/format";
import { toast } from "../../app/toast";
import { useQueueJobs, useQueueStats, useQueues, useSetDesiredWorkers } from "./api";
import type { JobRow, QueueState, QueueSummary } from "./types";

const QueueSparklines = lazy(() => import("./QueueSparklines"));

const STATES: QueueState[] = ["waiting", "running", "scheduled", "delayed", "retry", "failed", "dead"];

const jobColumns: ColumnDef<JobRow, unknown>[] = [
  { id: "id", header: "Job", cell: ({ row }) => <code>{row.original.id}</code> },
  { id: "workflow_key", header: "Workflow", cell: ({ row }) => row.original.workflow_key },
  { id: "attempts", header: "Attempts", cell: ({ row }) => row.original.attempts },
  {
    id: "error_class",
    header: "Error",
    cell: ({ row }) => {
      const ec = row.original.error_class;
      if (!ec) return <span className="qu-muted">—</span>;
      const known = (ERROR_CLASSES as readonly string[]).includes(ec);
      const info = known ? errorClassInfo(ec as ErrorClass) : { token: "neutral" as const, icon: "question" as const, label: ec };
      return <Badge status={info.token} label={info.label} icon={info.icon} />;
    },
  },
  { id: "created_at", header: "Age", cell: ({ row }) => relativeTime(row.original.created_at) },
];

export function QueueConsole({ name }: { name: string }) {
  useSseTopics(["queue", "worker"]);
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "";

  const queuesQ = useQueues();
  const statsQ = useQueueStats(name, "1m");
  const jobsQ = useQueueJobs(name, state);
  const setWorkers = useSetDesiredWorkers(name);
  const [replicas, setReplicas] = useState("");

  const summary: QueueSummary | undefined = queuesQ.data?.queues.find((q) => q.name === name);

  return (
    <RequireRole group="queues.fleet.read">
      <div className="page-header">
        <h1>Queue · {name}</h1>
        <Link to="/queues" className="qu-muted">
          ← all queues
        </Link>
      </div>

      <div className="qu-console-top">
        <StatTile label="oldest age" value={formatDurationS(summary?.oldest_age_s)} freshness="live" />
        {summary?.accumulating ? (
          <div className="qu-accumulating" role="status">
            <Badge status="warn" label="ACCUMULATING" icon="triangle" /> enqueue outpaces dequeue for 5+ buckets.
          </div>
        ) : null}
      </div>

      <section className="qu-panel">
        <h2 className="qu-panel-title">enqueue vs dequeue (per minute)</h2>
        {statsQ.isPending ? (
          <div className="skeleton" style={{ height: 200 }} aria-busy="true" />
        ) : statsQ.isError ? (
          <EmptyState variant="error" title="No stats" errorCode={isApiError(statsQ.error) ? statsQ.error.code : undefined} />
        ) : (
          <Suspense fallback={<div className="skeleton" style={{ height: 200 }} />}>
            <QueueSparklines points={statsQ.data.points} />
          </Suspense>
        )}
      </section>

      <section className="qu-panel">
        <h2 className="qu-panel-title">Jobs by state</h2>
        <div className="qu-state-tabs">
          {STATES.map((s) => (
            <button
              key={s}
              type="button"
              className="qu-state-tab"
              data-active={state === s || undefined}
              aria-pressed={state === s}
              onClick={() => setParams(state === s ? {} : { state: s })}
            >
              {s} <span className="qu-state-count">{formatCount(summary?.[s])}</span>
            </button>
          ))}
        </div>
        {!state ? (
          <p className="qu-muted">Pick a state to list its jobs (the endpoint requires a state filter).</p>
        ) : jobsQ.isPending ? (
          <div className="skeleton" style={{ height: 160 }} aria-busy="true" />
        ) : jobsQ.isError ? (
          <EmptyState variant="error" title="Could not load jobs" errorCode={isApiError(jobsQ.error) ? jobsQ.error.code : undefined} />
        ) : jobsQ.data.jobs.length === 0 ? (
          <EmptyState variant="zero-results" title={`No jobs in ${state}`} action={{ label: "Clear", onClick: () => setParams({}) }} />
        ) : (
          <Table columns={jobColumns} data={jobsQ.data.jobs} getRowId={(j) => j.id} caption={`${name} ${state} jobs`} />
        )}
      </section>

      <section className="qu-panel">
        <h2 className="qu-panel-title">Desired workers (intent)</h2>
        <div className="qu-workers-intent">
          <Input label="Desired replicas" value={replicas} onChange={setReplicas} type="number" />
          <Button
            variant="secondary"
            loading={setWorkers.isPending}
            onClick={() =>
              setWorkers.mutate(Number(replicas), {
                onSuccess: () => toast.info("Desired worker count recorded — deploy tooling actuates"),
                onError: (e) => toast.error(isApiError(e) ? e.message : "failed"),
              })
            }
          >
            Set intent
          </Button>
        </div>
        <p className="qu-muted">
          This records intent only. Actuation is deploy-tool territory (doc 06 honesty note) — the
          dashboard never scales workers directly.
        </p>
      </section>
    </RequireRole>
  );
}
