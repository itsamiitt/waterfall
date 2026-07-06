// features/workflows/NodeInspector.tsx — the node inspector (doc 09 §7.1). The waterfall_workflow
// schema carries retry/timeout/confidence/min-score/max-cost/max-providers at the WORKFLOW level
// (doc 07 §4); the inspector edits those bounds with the selected node as context. G3/G4 gate
// values render read-only context — the validator rejects overrides (VR-7/VR-9/VR-16).
import { Badge } from "../../design/primitives";
import type { RetryClass, WaterfallWorkflowPayload } from "./types";

const RETRY_CLASSES: RetryClass[] = ["TRANSIENT", "RATE_LIMIT", "PROVIDER_DOWN"];

export interface NodeInspectorProps {
  selected: string | null;
  payload: WaterfallWorkflowPayload;
  onChange: (next: WaterfallWorkflowPayload) => void;
  disabled?: boolean;
}

export function NodeInspector({ selected, payload, onChange, disabled }: NodeInspectorProps) {
  const retry = payload.retry_logic ?? {};

  function num(key: "timeout_ms" | "confidence_threshold" | "min_score" | "max_cost_credits" | "max_providers", raw: string) {
    const v = raw === "" ? undefined : Number(raw);
    onChange({ ...payload, [key]: v } as WaterfallWorkflowPayload);
  }
  function retryNum(key: "max_retries" | "backoff_ms", raw: string) {
    onChange({ ...payload, retry_logic: { ...retry, [key]: raw === "" ? undefined : Number(raw) } });
  }

  return (
    <div className="wf-inspector">
      <div className="wf-inspector-head">
        {selected ? (
          <>
            <span className="wf-muted">inspecting</span> <strong className="wf-provider">{selected}</strong>
          </>
        ) : (
          <span className="wf-muted">Select a node to inspect · editing workflow bounds</span>
        )}
      </div>

      <Field label="timeout_ms (250–120000)" value={payload.timeout_ms} step={250} disabled={disabled} onChange={(v) => num("timeout_ms", v)} />
      <Field label="confidence_threshold (0–1)" value={payload.confidence_threshold} step={0.05} disabled={disabled} onChange={(v) => num("confidence_threshold", v)} />
      <Field label="min_score (0–1)" value={payload.min_score} step={0.05} disabled={disabled} onChange={(v) => num("min_score", v)} />
      <Field label="max_cost_credits" value={payload.max_cost_credits} step={1} disabled={disabled} onChange={(v) => num("max_cost_credits", v)} />
      <Field label="max_providers (1–16)" value={payload.max_providers} step={1} disabled={disabled} onChange={(v) => num("max_providers", v)} />

      <div className="wf-inspector-group">
        <span className="wf-inspector-legend">retry_logic</span>
        <Field label="max_retries (0–3)" value={retry.max_retries} step={1} disabled={disabled} onChange={(v) => retryNum("max_retries", v)} />
        <Field label="backoff_ms (100–30000)" value={retry.backoff_ms} step={100} disabled={disabled} onChange={(v) => retryNum("backoff_ms", v)} />
        <div className="wf-retry-classes">
          {RETRY_CLASSES.map((c) => {
            const on = retry.retry_on?.includes(c) ?? false;
            return (
              <button
                key={c}
                type="button"
                className="wf-stop-chip"
                data-active={on || undefined}
                aria-pressed={on}
                disabled={disabled}
                onClick={() => {
                  const cur = retry.retry_on ?? [];
                  const next = on ? cur.filter((x) => x !== c) : [...cur, c];
                  onChange({ ...payload, retry_logic: { ...retry, retry_on: next.length ? next : undefined } });
                }}
              >
                {c}
              </button>
            );
          })}
        </div>
      </div>

      <p className="wf-gate-note">
        <Badge status="info" label="G3/G4" icon="shield" /> timeout, ceiling and provider caps are engine-enforced;
        config may tighten, never loosen — the validator rejects overrides.
      </p>
    </div>
  );
}

function Field({
  label,
  value,
  step,
  onChange,
  disabled,
}: {
  label: string;
  value: number | undefined;
  step: number;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  return (
    <label className="wf-field">
      <span>{label}</span>
      <input
        type="number"
        className="p-input"
        step={step}
        value={value ?? ""}
        disabled={disabled}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
    </label>
  );
}
