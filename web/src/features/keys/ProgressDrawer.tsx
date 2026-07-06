// features/keys — the shared bulk/import progress drawer (doc 09 §3.2/§3.3). Subscribes to the
// `import` SSE topic (import.batch.progress → invalidates the imports query root), so the bar and
// per-row error table live-update without polling. Screen → endpoint: GET /key-imports/{job_id}
// or GET /bulk-jobs/{id} (§4.3 progress schema; per-row errors capped at 1,000 + error_summary).
import { Drawer, EmptyState } from "../../design/primitives";
import { useSseTopics } from "../../api/sse";
import { isApiError } from "../../api/client";
import { formatCount } from "../../lib/format";
import { useBulkProgress, useImportProgress } from "./api";
import type { JobProgress } from "./types";

export interface ProgressDrawerProps {
  open: boolean;
  onClose: () => void;
  jobId: string | null;
  kind: "import" | "bulk";
}

export function ProgressDrawer({ open, onClose, jobId, kind }: ProgressDrawerProps) {
  useSseTopics(["import"]);
  const importQ = useImportProgress(kind === "import" ? jobId : null);
  const bulkQ = useBulkProgress(kind === "bulk" ? jobId : null);
  const q = kind === "import" ? importQ : bulkQ;
  const p: JobProgress | undefined = q.data;

  return (
    <Drawer open={open} onClose={onClose} title={kind === "import" ? "Import progress" : "Bulk job progress"}>
      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load job progress"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : !p ? (
        <div className="skeleton" style={{ height: 120 }} aria-busy="true" />
      ) : (
        <div className="section">
          <div className="detail-meta">
            <span>job <code>{p.job_id.slice(0, 12)}</code></span>
            <span>status <strong>{p.status}</strong></span>
          </div>
          <ProgressBar done={p.succeeded + p.failed} total={p.total} />
          <div className="detail-meta">
            <span>succeeded {formatCount(p.succeeded)}</span>
            <span>failed {formatCount(p.failed)}</span>
            <span>total {formatCount(p.total)}</span>
            {p.matched_at_execution != null ? <span>matched {formatCount(p.matched_at_execution)}</span> : null}
          </div>
          {p.errors && p.errors.length > 0 ? (
            <div className="section">
              <div className="section-title">Per-row errors{p.errors_truncated ? " (truncated at 1,000)" : ""}</div>
              <table className="p-table">
                <thead>
                  <tr><th scope="col">Row</th><th scope="col">Code</th><th scope="col">Message</th></tr>
                </thead>
                <tbody>
                  {p.errors.map((e, i) => (
                    <tr key={i}>
                      <td>{e.row ?? e.id ?? "—"}</td>
                      <td><code>{e.code}</code></td>
                      <td>{e.message}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
        </div>
      )}
    </Drawer>
  );
}

function ProgressBar({ done, total }: { done: number; total: number }) {
  const pct = total > 0 ? Math.min(100, (done / total) * 100) : 0;
  return (
    <div
      className="progress-track"
      role="progressbar"
      aria-valuenow={done}
      aria-valuemin={0}
      aria-valuemax={total}
      aria-label="Job progress"
    >
      <div className="progress-fill" style={{ width: `${pct}%` }} />
    </div>
  );
}
