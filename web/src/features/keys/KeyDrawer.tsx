// features/keys — per-key detail drawer (doc 09 §3.1). NO secret is ever shown — there is no
// reveal endpoint (doc 04 §2.4); only secret_last4 + fingerprint_prefix. Screen → endpoint:
// GET /keys/{id}, GET /keys/{id}/usage, POST /keys/{id}/{enable|disable|test|health-check|
// refresh-credits}, POST /keys/{id}/rotate (X-MFA-Code), DELETE /keys/{id}.
import { useState } from "react";
import { Button, ConfirmDialog, Drawer, EmptyState, Input } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { formatCount, formatLatencyMs, formatPercent, relativeTime } from "../../lib/format";
import { toast } from "../../app/toast";
import { KeyHealthBadge, KeyStatusBadge } from "./statusCells";
import { useArchiveKey, useKey, useKeyAction, useRotateKey, type KeyAction } from "./api";
import type { ProviderKey } from "./types";

const ACTIONS: { action: KeyAction; label: string }[] = [
  { action: "enable", label: "Enable" },
  { action: "disable", label: "Disable" },
  { action: "test", label: "Test" },
  { action: "health-check", label: "Health check" },
  { action: "refresh-credits", label: "Refresh credits" },
];

export function KeyDrawer({ keyId, open, onClose }: { keyId: string | null; open: boolean; onClose: () => void }) {
  const q = useKey(keyId ?? "");
  const act = useKeyAction(keyId ?? "");
  const rotate = useRotateKey(keyId ?? "");
  const archive = useArchiveKey(keyId ?? "");
  const [rotating, setRotating] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const [secret, setSecret] = useState("");
  const [overlap, setOverlap] = useState("86400");
  const [mfa, setMfa] = useState("");

  const k: ProviderKey | undefined = q.data;

  function doRotate() {
    rotate.mutate(
      { secret, overlap_s: Number(overlap) || 0, mfaCode: mfa || undefined },
      {
        onSuccess: (r) => {
          toast.success(`Rotated — successor ${r.successor_key_id.slice(0, 8)}, overlap until ${r.overlap_until}`);
          setRotating(false);
          setSecret("");
          setMfa("");
        },
      },
    );
  }

  return (
    <Drawer open={open} onClose={onClose} title={k?.label ?? "Key"}>
      {q.isError ? (
        <EmptyState
          variant="error"
          title="Could not load key"
          errorCode={isApiError(q.error) ? q.error.code : undefined}
          action={{ label: "Retry", onClick: () => void q.refetch() }}
        />
      ) : !k ? (
        <div className="skeleton" style={{ height: 200 }} aria-busy="true" />
      ) : (
        <div className="section">
          <div className="provider-badges">
            <KeyStatusBadge status={k.status} />
            <KeyHealthBadge health={k.health} />
          </div>
          <dl className="kv-list">
            <dt>last4</dt><dd>*{k.secret_last4 ?? "—"}</dd>
            <dt>fingerprint</dt><dd>{k.fingerprint_prefix ?? "—"}</dd>
            <dt>pool</dt><dd>{k.pool ?? "—"}</dd>
            <dt>region / env</dt><dd>{k.region ?? "—"} / {k.environment ?? "—"}</dd>
            <dt>weight / priority</dt><dd>{k.weight ?? "—"} / {k.priority ?? "—"}</dd>
            <dt>limits d/m/rpm</dt><dd>{k.daily_limit ?? "—"} / {k.monthly_limit ?? "—"} / {k.rpm_limit ?? "—"}</dd>
            <dt>credits</dt><dd>{formatCount(k.credits_remaining)}</dd>
            <dt>success / latency</dt><dd>{formatPercent(k.success_ewma)} / {formatLatencyMs(k.latency_ewma_ms)}</dd>
            <dt>consecutive fails</dt><dd>{k.consecutive_failures ?? 0}</dd>
            <dt>rotation group</dt><dd>{k.rotation_group ?? "—"}</dd>
            <dt>imported batch</dt><dd>{k.imported_batch_id ?? "—"}</dd>
            <dt>expires</dt><dd>{k.expires_at ?? "—"}</dd>
            <dt>last used</dt><dd>{k.last_used_at ? relativeTime(k.last_used_at) : "—"}</dd>
          </dl>

          <div className="action-bar">
            {ACTIONS.map((a) => (
              <Button key={a.action} size="sm" loading={act.isPending} onClick={() => act.mutate(a.action)}>
                {a.label}
              </Button>
            ))}
            <Button size="sm" onClick={() => setRotating((v) => !v)}>Rotate</Button>
            <Button size="sm" variant="danger" onClick={() => setConfirmArchive(true)}>Archive</Button>
          </div>

          {rotating ? (
            <div className="section">
              <div className="section-title">Rotate key</div>
              <Input label="New secret" value={secret} onChange={setSecret} mono autoComplete="off" />
              <Input label="Overlap seconds (0 = compromise mode)" value={overlap} onChange={setOverlap} inputMode="numeric" />
              <Input label="MFA code" value={mfa} onChange={setMfa} mono description="Required for rotate (doc 05 §5.4)" />
              <Button variant="primary" loading={rotate.isPending} disabled={secret === ""} onClick={doRotate}>
                Rotate now
              </Button>
            </div>
          ) : null}
        </div>
      )}

      <ConfirmDialog
        open={confirmArchive}
        onClose={() => setConfirmArchive(false)}
        onConfirm={() => {
          setConfirmArchive(false);
          archive.mutate(undefined, { onSuccess: () => { toast.success("Key archived"); onClose(); } });
        }}
        title="Archive this key?"
        body="Soft, terminal archive. Usage rollups are retained for cost attribution."
        consequences={
          k
            ? [`last used ${k.last_used_at ? relativeTime(k.last_used_at) : "never"}`, `${formatCount(k.usage_today)} calls today`]
            : undefined
        }
        confirmLabel="Archive"
        danger
        busy={archive.isPending}
      />
    </Drawer>
  );
}
