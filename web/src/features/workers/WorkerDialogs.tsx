// features/workers/WorkerDialogs.tsx — drain, rolling-restart, and scale-intent dialogs (doc 09
// §9). Drain ≠ stop: in-flight Jobs hold leased Provider Keys + reserved credits (doc 06). Scale
// and rolling-restart carry the honest "actuation is deploy-layer" note.
import { useState } from "react";
import { Button, Input, Modal } from "../../design/primitives";
import type { RollingRestartRequest, ScaleRequest, Worker } from "./types";

export function DrainDialog({
  worker,
  open,
  onClose,
  onConfirm,
  busy,
}: {
  worker: Worker | null;
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  busy: boolean;
}) {
  return (
    <Modal
      open={open && worker !== null}
      onClose={onClose}
      title={`Drain ${worker?.id ?? ""}`}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} onClick={onConfirm}>
            Drain
          </Button>
        </>
      }
    >
      <p>
        Drain finishes the {worker?.jobs_active ?? 0} in-flight Enrichment Jobs — they hold leased
        Provider Keys and reserved credits — then stops. Stop (restart) instead abandons them to the
        visibility-timeout reclaim path.
      </p>
      <p className="wk-muted">
        jobs_active is live: <strong>{worker?.jobs_active ?? 0}</strong> → watch it fall.
      </p>
    </Modal>
  );
}

export function RollingRestartDialog({
  open,
  onClose,
  onConfirm,
  busy,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: (req: RollingRestartRequest) => void;
  busy: boolean;
}) {
  const [kind, setKind] = useState("");
  const [queue, setQueue] = useState("");
  const [maxUnavailable, setMax] = useState("2");
  const n = Number(maxUnavailable);
  const valid = Number.isFinite(n) && n >= 1;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Rolling restart"
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!valid}
            onClick={() => onConfirm({ kind: kind || undefined, queue: queue || undefined, max_unavailable: n })}
          >
            Start
          </Button>
        </>
      }
    >
      <p>Sequenced drains — never more than max_unavailable workers down at once. Returns a 202 job.</p>
      <div className="wk-dialog-fields">
        <Input label="kind (blank = all)" value={kind} onChange={setKind} placeholder="enrich" />
        <Input label="queue (blank = all)" value={queue} onChange={setQueue} placeholder="enrich-bulk" />
        <Input label="max_unavailable" value={maxUnavailable} onChange={setMax} type="number" error={valid ? undefined : "must be ≥ 1"} />
      </div>
    </Modal>
  );
}

export function ScaleDialog({
  open,
  onClose,
  onConfirm,
  busy,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: (req: ScaleRequest) => void;
  busy: boolean;
}) {
  const [kind, setKind] = useState("");
  const [queue, setQueue] = useState("");
  const [replicas, setReplicas] = useState("");
  const n = Number(replicas);
  const valid = kind !== "" && queue !== "" && Number.isFinite(n) && n >= 0;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Scale intent"
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => onConfirm({ kind, queue, replicas: n })}>
            Record intent
          </Button>
        </>
      }
    >
      <p className="wk-honest">
        This records intent only — deploy tooling actuates (doc 06). The dashboard never scales
        workers directly.
      </p>
      <div className="wk-dialog-fields">
        <Input label="kind" value={kind} onChange={setKind} placeholder="enrich" required />
        <Input label="queue" value={queue} onChange={setQueue} placeholder="enrich-default" required />
        <Input label="replicas" value={replicas} onChange={setReplicas} type="number" required />
      </div>
    </Modal>
  );
}
