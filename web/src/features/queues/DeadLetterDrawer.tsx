// features/queues/DeadLetterDrawer.tsx — the DLQ inspection drawer (doc 09 §8.1): payload in a
// CodeBlock (principal fields redacted server-side), last_error, attempts, and the redrive button
// with the G2-idempotency explainer copy. A 404 on redrive renders as "already redriven or gone".
import { useState, type ReactElement } from "react";
import type { UseMutationResult } from "@tanstack/react-query";
import { isApiError } from "../../api/client";
import { Badge, Button, CodeBlock, ConfirmDialog, Drawer } from "../../design/primitives";
import { toast } from "../../app/toast";
import { useJobDetail } from "./api";
import { REDRIVE_ALREADY_GONE, REDRIVE_EXPLAINER, redriveConsequences } from "./redrive";
import type { DeadLetter, RedriveResult } from "./types";

export interface DeadLetterDrawerProps {
  jobId: string | null;
  onClose: () => void;
  job: DeadLetter | undefined;
  redrive: UseMutationResult<RedriveResult, Error, string>;
}

export function DeadLetterDrawer({ jobId, onClose, job, redrive }: DeadLetterDrawerProps): ReactElement | null {
  const [confirm, setConfirm] = useState(false);
  const detailQ = useJobDetail(jobId ?? undefined);

  if (!jobId) return null;

  return (
    <Drawer open={jobId !== null} onClose={onClose} title={`Dead letter ${jobId}`} width={520}>
      <div className="qu-dl-drawer">
        <div className="qu-dl-meta">
          <Badge status="error" label={`dead=true`} icon="archive" />
          {job ? <span>attempts {job.attempts}</span> : null}
        </div>
        {job?.last_error ? (
          <p className="qu-dl-lasterror">
            <strong>last_error:</strong> {job.last_error}
          </p>
        ) : null}

        <h3 className="qu-dl-subhead">Payload</h3>
        {detailQ.isPending ? (
          <div className="skeleton" style={{ height: 160 }} aria-busy="true" />
        ) : detailQ.isError ? (
          <p className="qu-dl-lasterror">Could not load payload: {isApiError(detailQ.error) ? detailQ.error.code : "error"}</p>
        ) : (
          <CodeBlock code={JSON.stringify(detailQ.data?.payload ?? {}, null, 2)} language="json" copyable />
        )}

        <div className="qu-dl-actions">
          <Button variant="primary" onClick={() => setConfirm(true)} loading={redrive.isPending}>
            Redrive this job
          </Button>
        </div>
        <p className="qu-muted">{REDRIVE_EXPLAINER}</p>
      </div>

      {job ? (
        <ConfirmDialog
          open={confirm}
          onClose={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false);
            redrive.mutate(job.id, {
              onSuccess: () => {
                toast.success(`Redriven ${job.id}`);
                onClose();
              },
              onError: (e) => {
                if (isApiError(e) && e.status === 404) {
                  toast.info(REDRIVE_ALREADY_GONE);
                  onClose();
                } else {
                  toast.error(isApiError(e) ? e.message : "redrive failed");
                }
              },
            });
          }}
          title={`Redrive ${job.id}`}
          body={REDRIVE_EXPLAINER}
          consequences={redriveConsequences(job)}
          confirmLabel="Redrive"
          busy={redrive.isPending}
        />
      ) : null}
    </Drawer>
  );
}
