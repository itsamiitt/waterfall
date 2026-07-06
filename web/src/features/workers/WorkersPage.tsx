// features/workers/WorkersPage.tsx — /workers. Fleet grid showing status vs desired_state as two
// columns with a converging badge, drain/restart split actions, a rolling-restart dialog with
// max-unavailable, and scale intent (doc 09 §9). Live via worker.state.changed.
import { useState } from "react";
import { isApiError } from "../../api/client";
import { Badge, Button, EmptyState, Input, Table, type ColumnDef } from "../../design/primitives";
import { RequireRole, useAuth } from "../../app/guards";
import { can } from "../../lib/permissions";
import { useSseTopics } from "../../api/sse";
import { workerStatusInfo } from "../../lib/status";
import { formatDurationS, relativeTime } from "../../lib/format";
import { toast } from "../../app/toast";
import { workerConvergence } from "./convergence";
import { DrainDialog, RollingRestartDialog, ScaleDialog } from "./WorkerDialogs";
import { useRollingRestart, useScale, useWorkerAction, useWorkers } from "./api";
import type { Worker, WorkerFilters } from "./types";

export function WorkersPage() {
  useSseTopics(["worker"]);
  const [filters, setFilters] = useState<WorkerFilters>({});
  const q = useWorkers(filters);

  const action = useWorkerAction();
  const scale = useScale();
  const rolling = useRollingRestart();

  const [drainTarget, setDrainTarget] = useState<Worker | null>(null);
  const [showRolling, setShowRolling] = useState(false);
  const [showScale, setShowScale] = useState(false);

  function doAction(id: string, act: "restart" | "pause" | "resume") {
    action.mutate(
      { id, action: act },
      {
        onSuccess: () => toast.info(`${act} requested — worker converges via heartbeat`),
        onError: (e) => toast.error(isApiError(e) ? e.message : `${act} failed`),
      },
    );
  }

  const columns: ColumnDef<Worker, unknown>[] = [
    { id: "id", header: "Worker", cell: ({ row }) => <code>{row.original.id}</code> },
    { id: "kind", header: "Kind", cell: ({ row }) => row.original.kind },
    { id: "queue", header: "Queue", cell: ({ row }) => row.original.queue },
    {
      id: "status",
      header: "Status",
      cell: ({ row }) => {
        const info = workerStatusInfo(row.original.status);
        return <Badge status={info.token} label={info.label} icon={info.icon} />;
      },
    },
    {
      id: "desired_state",
      header: "Desired",
      cell: ({ row }) => <code className="wk-desired">{row.original.desired_state}</code>,
    },
    {
      id: "converging",
      header: "Convergence",
      cell: ({ row }) => {
        const w = row.original;
        const c = workerConvergence(w);
        if (w.status === "lost") {
          return (
            <span className="wk-conv">
              <Badge status={c.token} label={c.label} icon={c.icon} />
              <span className="wk-muted">{relativeTime(w.last_heartbeat_at)}</span>
            </span>
          );
        }
        if (!c.converging) return <span className="wk-muted">converged</span>;
        return (
          <span className="wk-conv">
            <Badge status={c.token} label={c.label} icon={c.icon} />
            {w.converging_for_s !== undefined ? (
              <span className="wk-muted">{formatDurationS(w.converging_for_s)}</span>
            ) : null}
            {c.escalated ? <span className="wk-runbook">see runbook: lost workers</span> : null}
          </span>
        );
      },
    },
    { id: "jobs_active", header: "Jobs", cell: ({ row }) => row.original.jobs_active },
    {
      id: "cpu",
      header: "CPU / Mem",
      cell: ({ row }) => (
        <span className="wk-muted">
          {row.original.cpu_pct !== undefined ? `${row.original.cpu_pct}%` : "—"} /{" "}
          {row.original.mem_mb !== undefined ? `${row.original.mem_mb}M` : "—"}
        </span>
      ),
    },
  ];

  return (
    <RequireRole group="workers.read">
      <div className="page-header">
        <h1>Workers</h1>
        {q.data ? <span className="page-header-meta">{q.data.workers.length} registered</span> : null}
        <div className="wk-header-actions">
          <RequireActions>
            <Button variant="secondary" onClick={() => setShowScale(true)}>
              Scale intent
            </Button>
            <Button variant="secondary" onClick={() => setShowRolling(true)}>
              Rolling restart
            </Button>
          </RequireActions>
        </div>
      </div>

      <div className="wk-filters">
        <Input label="kind" value={filters.kind ?? ""} onChange={(v) => setFilters((f) => ({ ...f, kind: v }))} />
        <Input label="queue" value={filters.queue ?? ""} onChange={(v) => setFilters((f) => ({ ...f, queue: v }))} />
        <Input label="region" value={filters.region ?? ""} onChange={(v) => setFilters((f) => ({ ...f, region: v }))} />
      </div>

      {q.isPending ? (
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" />
      ) : q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load workers"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          body={q.error instanceof Error ? q.error.message : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.data.workers.length === 0 ? (
        <EmptyState
          variant={Object.values(filters).some(Boolean) ? "zero-results" : "zero-data"}
          title={Object.values(filters).some(Boolean) ? "No workers match filters" : "No workers have ever registered"}
          body="Workers register on deploy — see the deployment docs (doc 11)."
          action={Object.values(filters).some(Boolean) ? { label: "Clear filters", onClick: () => setFilters({}) } : undefined}
        />
      ) : (
        <Table
          columns={columns}
          data={q.data.workers}
          getRowId={(w) => w.id}
          caption="Worker fleet"
          rowActions={(w) => (
            <span className="wk-row-actions">
              <Button size="sm" variant="secondary" onClick={() => setDrainTarget(w)}>
                Drain
              </Button>
              <Button size="sm" variant="secondary" onClick={() => doAction(w.id, "restart")}>
                Restart
              </Button>
              {w.status === "paused" ? (
                <Button size="sm" variant="ghost" onClick={() => doAction(w.id, "resume")}>
                  Resume
                </Button>
              ) : (
                <Button size="sm" variant="ghost" onClick={() => doAction(w.id, "pause")}>
                  Pause
                </Button>
              )}
            </span>
          )}
        />
      )}

      <DrainDialog
        worker={drainTarget}
        open={drainTarget !== null}
        onClose={() => setDrainTarget(null)}
        busy={action.isPending}
        onConfirm={() => {
          const w = drainTarget;
          if (!w) return;
          action.mutate(
            { id: w.id, action: "drain" },
            {
              onSuccess: () => {
                toast.info(`Draining ${w.id}`);
                setDrainTarget(null);
              },
              onError: (e) => {
                toast.error(isApiError(e) ? e.message : "drain failed");
                setDrainTarget(null);
              },
            },
          );
        }}
      />

      <RollingRestartDialog
        open={showRolling}
        onClose={() => setShowRolling(false)}
        busy={rolling.isPending}
        onConfirm={(req) =>
          rolling.mutate(req, {
            onSuccess: (r) => {
              toast.success(`Rolling restart job ${r.job_id} started`);
              setShowRolling(false);
            },
            onError: (e) => toast.error(isApiError(e) ? e.message : "rolling restart failed"),
          })
        }
      />

      <ScaleDialog
        open={showScale}
        onClose={() => setShowScale(false)}
        busy={scale.isPending}
        onConfirm={(req) =>
          scale.mutate(req, {
            onSuccess: () => {
              toast.info("Scale intent recorded — deploy tooling actuates");
              setShowScale(false);
            },
            onError: (e) => toast.error(isApiError(e) ? e.message : "scale failed"),
          })
        }
      />
    </RequireRole>
  );
}

/** Fleet actions require workers.actions; hide them otherwise (server re-authorizes anyway). */
function RequireActions({ children }: { children: React.ReactNode }) {
  const { role } = useAuth();
  if (!can(role, "workers.actions")) return null;
  return <>{children}</>;
}
