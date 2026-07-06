// features/rotation — /rotation engine view (doc 09 §4.1). Screen → endpoint:
// GET /rotation/strategies (catalog of 12) + GET/PUT /rotation/triggers (error-class → KM-3
// transition config; validators reject disabling AUTH → auth_failed handling → 422 inline,
// doc 04 §2.5).
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { Badge, Button, EmptyState, Input } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { errorClassInfo, ERROR_CLASSES, type ErrorClass } from "../../lib/status";
import { useStrategies, useTriggers, usePutTriggers } from "./api";
import type { TriggerRule } from "./types";

const isErrorClass = (s: string): s is ErrorClass => (ERROR_CLASSES as readonly string[]).includes(s);

function StrategyCatalog() {
  const q = useStrategies();
  if (q.isError) {
    return (
      <EmptyState variant="error" title="Could not load strategies"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }} />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 160 }} aria-busy="true" />;
  return (
    <div className="section">
      <div className="section-title">Strategy catalog ({q.data.strategies.length})</div>
      <div className="strat-grid">
        {q.data.strategies.map((s) => (
          <div key={s.id} className="strat-card">
            <Badge status="info" label={s.label ?? s.id} icon="refresh" />
            {s.description ? <p className="p-field-description">{s.description}</p> : null}
          </div>
        ))}
      </div>
    </div>
  );
}

function TriggerForm() {
  const q = useTriggers();
  const put = usePutTriggers();
  const [rules, setRules] = useState<TriggerRule[]>([]);

  useEffect(() => {
    if (q.data) setRules(q.data.rules);
  }, [q.data]);

  if (q.isError) {
    return (
      <EmptyState variant="error" title="Could not load triggers"
        errorCode={isApiError(q.error) ? q.error.code : undefined}
        action={{ label: "Retry", onClick: () => void q.refetch() }} />
    );
  }
  if (q.isPending) return <div className="skeleton" style={{ height: 200 }} aria-busy="true" />;

  const setCooldown = (i: number, v: string) =>
    setRules((rs) => rs.map((r, j) => (j === i ? { ...r, cooldown_s: Number(v) || 0 } : r)));

  return (
    <div className="section">
      <div className="section-title">Rotation triggers (error class → KM-3 transition)</div>
      <table className="p-table">
        <thead>
          <tr><th scope="col">Error class</th><th scope="col">Transition</th><th scope="col">Cooldown (s)</th><th scope="col">Auto</th></tr>
        </thead>
        <tbody>
          {rules.map((r, i) => {
            const info = isErrorClass(r.error_class) ? errorClassInfo(r.error_class) : null;
            return (
              <tr key={r.error_class}>
                <td>{info ? <Badge status={info.token} label={info.label} icon={info.icon} /> : r.error_class}</td>
                <td><code>{r.transition}</code></td>
                <td style={{ maxWidth: 120 }}>
                  <Input label="" value={String(r.cooldown_s ?? 0)} onChange={(v) => setCooldown(i, v)} inputMode="numeric" />
                </td>
                <td>{r.auto ? "auto" : "manual"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="action-bar">
        <Button size="sm" variant="primary" loading={put.isPending}
          onClick={() => put.mutate({ rules }, { onSuccess: () => toast.success("Triggers updated") })}>
          Save thresholds
        </Button>
      </div>
    </div>
  );
}

export default function RotationView() {
  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Rotation engine</h1>
        <span className="page-header-meta"><Link to="/key-pools">← key pools</Link></span>
      </div>
      <StrategyCatalog />
      <TriggerForm />
    </>
  );
}
