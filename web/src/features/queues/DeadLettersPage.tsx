// features/queues/DeadLettersPage.tsx — /dead-letters. Parked-job list with error-class filter,
// the redrive drawer (payload CodeBlock, last_error, attempts, redrive with the G2 explainer),
// and filtered replay → 202 (doc 09 §8). "No dead letters" is a GOOD empty state (doc 09 §8.3).
import { useState } from "react";
import { isApiError } from "../../api/client";
import {
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Select,
  Table,
  type ColumnDef,
} from "../../design/primitives";
import { RequireRole } from "../../app/guards";
import { useSseTopics } from "../../api/sse";
import { ERROR_CLASSES, errorClassInfo, type ErrorClass } from "../../lib/status";
import { relativeTime } from "../../lib/format";
import { toast } from "../../app/toast";
import { DeadLetterDrawer } from "./DeadLetterDrawer";
import { useDeadLetters, useRedrive, useReplay } from "./api";
import { replaySummary } from "./redrive";
import type { DeadLetter } from "./types";

const ERROR_OPTS: { value: ErrorClass | ""; label: string }[] = [
  { value: "", label: "All error classes" },
  ...ERROR_CLASSES.map((c) => ({ value: c, label: c })),
];

export function DeadLettersPage() {
  useSseTopics(["queue"]);
  const [errorClass, setErrorClass] = useState<ErrorClass | "">("");
  const [openId, setOpenId] = useState<string | null>(null);
  const [confirmReplay, setConfirmReplay] = useState(false);

  const q = useDeadLetters(errorClass ? { error_class: errorClass } : {});
  const replay = useReplay("enrich-default"); // replay targets the queue the parked jobs belong to
  const redrive = useRedrive();

  const columns: ColumnDef<DeadLetter, unknown>[] = [
    { id: "id", header: "Job", cell: ({ row }) => <code>{row.original.id}</code> },
    { id: "workflow_key", header: "Workflow", cell: ({ row }) => row.original.workflow_key },
    { id: "attempts", header: "Attempts", cell: ({ row }) => row.original.attempts },
    {
      id: "error_class",
      header: "Last error",
      cell: ({ row }) => {
        const ec = row.original.error_class;
        const known = ec && (ERROR_CLASSES as readonly string[]).includes(ec);
        return (
          <span className="qu-dl-error">
            {known ? (
              <Badge {...badgeProps(ec as ErrorClass)} />
            ) : ec ? (
              <Badge status="neutral" label={ec} icon="question" />
            ) : null}
            <span className="qu-muted">{row.original.last_error}</span>
          </span>
        );
      },
    },
    { id: "created_at", header: "Age", cell: ({ row }) => relativeTime(row.original.created_at) },
  ];

  return (
    <RequireRole group="dead_letters.read">
      <div className="page-header">
        <h1>Dead letters</h1>
        {q.data ? <span className="page-header-meta">{q.data.dead_letters.length} parked</span> : null}
      </div>

      <div className="qu-dl-toolbar">
        <Select label="Error class" options={ERROR_OPTS} value={errorClass} onChange={setErrorClass} />
        <Button
          variant="secondary"
          disabled={(q.data?.dead_letters.length ?? 0) === 0}
          onClick={() => setConfirmReplay(true)}
        >
          Replay all matching filter
        </Button>
      </div>

      {q.isPending ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" />
      ) : q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load dead letters"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : q.data.dead_letters.length === 0 ? (
        <EmptyState
          variant="zero-data"
          title="No dead letters — nothing is parked"
          body="A healthy queue. Jobs only land here after exhausting retries."
        />
      ) : (
        <Table
          columns={columns}
          data={q.data.dead_letters}
          getRowId={(d) => d.id}
          onRowActivate={(d) => setOpenId(d.id)}
          rowActions={(d) => (
            <Button size="sm" variant="ghost" onClick={() => setOpenId(d.id)}>
              Inspect
            </Button>
          )}
          caption="Dead-lettered jobs"
        />
      )}

      <DeadLetterDrawer
        jobId={openId}
        onClose={() => setOpenId(null)}
        job={q.data?.dead_letters.find((d) => d.id === openId)}
        redrive={redrive}
      />

      <ConfirmDialog
        open={confirmReplay}
        onClose={() => setConfirmReplay(false)}
        onConfirm={() => {
          setConfirmReplay(false);
          replay.mutate(
            { error_class: errorClass ? [errorClass] : undefined },
            {
              onSuccess: (r) => toast.success(`Replay job ${r.job_id} started`),
              onError: (e) => toast.error(isApiError(e) ? e.message : "replay failed"),
            },
          );
        }}
        title="Replay matching dead letters"
        body="The replay job re-evaluates this filter under RLS at execution time (matched set may differ). Re-execution is G2-safe via the Idempotency-Key ledger."
        consequences={[replaySummary({ error_class: errorClass ? [errorClass] : undefined }), "Rate-limited; returns a 202 job"]}
        confirmLabel="Start replay"
        busy={replay.isPending}
      />
    </RequireRole>
  );
}

function badgeProps(ec: ErrorClass) {
  const info = errorClassInfo(ec);
  return { status: info.token, label: info.label, icon: info.icon } as const;
}
