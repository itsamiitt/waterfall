// features/workflows/WorkflowCanvas.tsx — the stepped builder canvas (doc 09 §7.1):
// entry → parallel group → sequential → fallback. Sequential legs are dnd-kit sortable;
// validation errors anchor to the offending node (red outline + message, doc 09 §7.3).
import { useState } from "react";
import { Badge, Button, Input } from "../../design/primitives";
import { SortableSeq } from "./SortableSeq";
import { canvasNodes, reorderSequential } from "./lifecycle";
import type { StopCondition, WaterfallWorkflowPayload } from "./types";

const STOP_CONDITIONS: StopCondition[] = ["target-met", "ceiling", "exhausted", "timeout"];

export interface WorkflowCanvasProps {
  payload: WaterfallWorkflowPayload;
  onChange: (next: WaterfallWorkflowPayload) => void;
  selected: string | null;
  onSelect: (provider: string) => void;
  nodeErrors: Map<string, string>;
  disabled?: boolean;
}

export function WorkflowCanvas({
  payload,
  onChange,
  selected,
  onSelect,
  nodeErrors,
  disabled,
}: WorkflowCanvasProps) {
  const nodes = canvasNodes(payload);
  const parallel = payload.parallel_providers ?? [];
  const sequential = payload.sequential_providers ?? [];

  function errorFor(step: string, index: number): string | undefined {
    // The validator anchors excluded/cycle errors on payload paths; surface the first match.
    return nodeErrors.get(`/${step}`) ?? nodeErrors.get(`/${step}/${index}`);
  }

  function NodeChip({ provider, step, index }: { provider: string; step: string; index: number }) {
    const err = errorFor(step, index);
    return (
      <button
        type="button"
        className="wf-node"
        data-selected={selected === provider || undefined}
        data-invalid={err ? true : undefined}
        aria-pressed={selected === provider}
        onClick={() => onSelect(provider)}
        title={err}
      >
        <span className="wf-provider">{provider}</span>
        {err ? <span className="wf-node-err" role="alert">{err}</span> : null}
      </button>
    );
  }

  return (
    <div className="wf-canvas">
      <div className="wf-steps">
        <Step title="Entry">
          {payload.entry_provider ? (
            <NodeChip provider={payload.entry_provider} step="entry_provider" index={0} />
          ) : (
            <Empty />
          )}
          {!disabled ? (
            <ProviderAdder
              label="Set entry provider"
              onAdd={(p) => onChange({ ...payload, entry_provider: p })}
            />
          ) : null}
        </Step>

        <Arrow />

        <Step title="Parallel group" note="cheap prefix · 2–4 (VR-6)">
          {parallel.length === 0 ? <Empty /> : null}
          <div className="wf-parallel">
            {parallel.map((p, i) => (
              <div key={p} className="wf-node-wrap">
                <NodeChip provider={p} step="parallel_providers" index={i} />
                {!disabled ? (
                  <Button size="sm" variant="ghost" onClick={() => onChange({ ...payload, parallel_providers: parallel.filter((x) => x !== p) })}>
                    ×
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
          {!disabled ? (
            <ProviderAdder
              label="Add parallel provider"
              onAdd={(p) => !parallel.includes(p) && onChange({ ...payload, parallel_providers: [...parallel, p] })}
            />
          ) : null}
        </Step>

        <Arrow />

        <Step title="Sequential" note="ordered fall-through">
          {sequential.length === 0 ? <Empty /> : null}
          <SortableSeq
            items={sequential}
            ariaLabel="Sequential providers"
            disabled={disabled}
            onReorder={(a, o) => onChange(reorderSequential(payload, a, o))}
            renderItem={(p) => {
              const idx = sequential.indexOf(p);
              return (
                <span className="wf-seq-row">
                  <NodeChip provider={p} step="sequential_providers" index={idx} />
                  {!disabled ? (
                    <Button size="sm" variant="ghost" onClick={() => onChange({ ...payload, sequential_providers: sequential.filter((x) => x !== p) })}>
                      ×
                    </Button>
                  ) : null}
                </span>
              );
            }}
          />
          {!disabled ? (
            <ProviderAdder
              label="Add sequential provider"
              onAdd={(p) => !sequential.includes(p) && onChange({ ...payload, sequential_providers: [...sequential, p] })}
            />
          ) : null}
        </Step>

        <Arrow />

        <Step title="Fallback" note="tried last, once">
          {payload.fallback_provider ? (
            <NodeChip provider={payload.fallback_provider} step="fallback_provider" index={0} />
          ) : (
            <Empty />
          )}
          {!disabled ? (
            <ProviderAdder
              label="Set fallback provider"
              onAdd={(p) => onChange({ ...payload, fallback_provider: p })}
            />
          ) : null}
        </Step>
      </div>

      <div className="wf-stops">
        <span className="wf-stops-label">stop_conditions (VR-11: non-empty):</span>
        {STOP_CONDITIONS.map((c) => {
          const on = payload.stop_conditions.includes(c);
          return (
            <button
              key={c}
              type="button"
              className="wf-stop-chip"
              data-active={on || undefined}
              aria-pressed={on}
              disabled={disabled}
              onClick={() =>
                onChange({
                  ...payload,
                  stop_conditions: on
                    ? payload.stop_conditions.filter((x) => x !== c)
                    : [...payload.stop_conditions, c],
                })
              }
            >
              {c}
            </button>
          );
        })}
        {payload.stop_conditions.length === 0 ? (
          <span className="wf-node-err" role="alert">VR-11: at least one stop condition is required</span>
        ) : null}
      </div>

      {nodes.length === 0 ? (
        <p className="wf-muted">Empty canvas — add an entry Provider to begin.</p>
      ) : null}
    </div>
  );
}

function Step({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <div className="wf-step">
      <div className="wf-step-head">
        <span className="wf-step-title">{title}</span>
        {note ? <span className="wf-muted">{note}</span> : null}
      </div>
      <div className="wf-step-body">{children}</div>
    </div>
  );
}

function Arrow() {
  return <div className="wf-arrow" aria-hidden="true">→</div>;
}

function Empty() {
  return <Badge status="neutral" label="empty" icon="dot" />;
}

function ProviderAdder({ label, onAdd }: { label: string; onAdd: (provider: string) => void }) {
  const [v, setV] = useState("");
  return (
    <div className="wf-adder">
      <Input label={label} value={v} onChange={setV} placeholder="provider-slug" />
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          const id = v.trim().toLowerCase();
          if (id) onAdd(id);
          setV("");
        }}
      >
        Set
      </Button>
    </div>
  );
}
