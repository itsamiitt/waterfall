// features/alerts/RuleEditor.tsx — the alert RULE BUILDER (doc 09 §12.1). Built field-by-field
// from CLOSED-VOCABULARY pickers (doc 10 §4): metric / op / threshold / window / cooldown /
// severity are selects and numeric inputs — there is NO query language and NO free metric box
// (doc 04 §2.11: out-of-vocab metric → 422). Presentational: data hooks live in the route wrapper
// so this form renders (and unit-tests) without a QueryClient.
import { useState } from "react";
import { Button, Input, Select, type SelectOption } from "../../design/primitives";
import type { AlertChannel } from "./types";
import type { AlertRuleInput } from "./types";
import { METRIC_BY_ID, METRIC_VOCAB, OPS, SEVERITIES, type Severity } from "./vocab";

/** Seed a rule from a metric's doc 10 §4 defaults (also used by tests). */
export function defaultRuleInput(metric = METRIC_VOCAB[0]!.metric): AlertRuleInput {
  const spec = METRIC_BY_ID.get(metric)!;
  return {
    name: "",
    metric,
    scope: {},
    op: spec.defaultOp,
    threshold: spec.defaultThreshold ?? 0,
    window_s: spec.defaultWindowS ?? 0,
    cooldown_s: 3600,
    severity: "critical",
    channels: [],
    enabled: true,
  };
}

const metricOpts: SelectOption[] = METRIC_VOCAB.map((m) => ({ value: m.metric, label: m.metric }));
const opOpts: SelectOption[] = OPS.map((o) => ({ value: o.value, label: o.label }));
const sevOpts: SelectOption[] = SEVERITIES.map((s) => ({ value: s, label: s }));

export interface RuleEditorProps {
  initial: AlertRuleInput;
  channels: AlertChannel[];
  saving?: boolean;
  testing?: boolean;
  testMessage?: string;
  errorMessage?: string;
  onSubmit: (input: AlertRuleInput) => void;
  onTest?: (input: AlertRuleInput) => void;
}

export function RuleEditor({
  initial,
  channels,
  saving,
  testing,
  testMessage,
  errorMessage,
  onSubmit,
  onTest,
}: RuleEditorProps) {
  const [r, setR] = useState<AlertRuleInput>(initial);
  const spec = METRIC_BY_ID.get(r.metric) ?? METRIC_VOCAB[0]!;
  const isSustained = r.metric === "provider.error_rate";

  function selectMetric(metric: string) {
    // Changing the metric re-seeds op/threshold/window from that metric's defaults and clears scope
    // (each metric has its own allowed scope keys — doc 10 §4).
    const next = defaultRuleInput(metric);
    setR((prev) => ({ ...next, name: prev.name, severity: prev.severity, channels: prev.channels }));
  }

  function toggleChannel(id: string) {
    setR((prev) => ({
      ...prev,
      channels: prev.channels.includes(id)
        ? prev.channels.filter((c) => c !== id)
        : [...prev.channels, id],
    }));
  }

  return (
    <form
      className="rule-editor"
      style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)", maxWidth: 560 }}
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(r);
      }}
    >
      <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
        Field-by-field builder — no query language; the metric is a closed vocabulary (doc 10 §4).
      </p>

      <Input label="Name" value={r.name} onChange={(v) => setR({ ...r, name: v })} required />

      <Select label="Metric" options={metricOpts} value={r.metric} onChange={selectMetric} />

      {spec.scopeKeys.length === 0 ? (
        <p style={{ fontSize: "var(--text-sm)", color: "var(--color-text-muted)" }}>
          Platform-scoped metric (operator-only) — no scope keys.
        </p>
      ) : (
        spec.scopeKeys.map((k) => (
          <Input
            key={k}
            label={`scope · ${k}`}
            value={r.scope[k] ?? ""}
            onChange={(v) => setR({ ...r, scope: { ...r.scope, [k]: v } })}
          />
        ))
      )}

      <div style={{ display: "flex", gap: "var(--space-3)", flexWrap: "wrap" }}>
        <Select label="Operator" options={opOpts} value={r.op} onChange={(v) => setR({ ...r, op: v as AlertRuleInput["op"] })} />
        <Input
          label={`Threshold (${spec.unit})`}
          value={String(r.threshold)}
          inputMode="decimal"
          onChange={(v) => setR({ ...r, threshold: Number(v) || 0 })}
        />
      </div>

      <div style={{ display: "flex", gap: "var(--space-3)", flexWrap: "wrap" }}>
        <Input
          label="window_s"
          value={String(r.window_s)}
          inputMode="numeric"
          disabled={!spec.windowApplies}
          description={spec.windowApplies ? undefined : "point-in-time metric — window ignored"}
          onChange={(v) => setR({ ...r, window_s: Number(v) || 0 })}
        />
        <Input
          label="cooldown_s"
          value={String(r.cooldown_s)}
          inputMode="numeric"
          onChange={(v) => setR({ ...r, cooldown_s: Number(v) || 0 })}
        />
      </div>

      {isSustained ? (
        <p style={{ fontSize: "var(--text-sm)", color: "var(--color-text-muted)" }}>
          "sustained" — breach must hold over both the last 5m and the full window_s (dual-window, doc 10 §5.1).
        </p>
      ) : null}

      <Select label="Severity" options={sevOpts} value={r.severity} onChange={(v) => setR({ ...r, severity: v as Severity })} />

      <fieldset style={{ border: "1px solid var(--color-border)", borderRadius: "var(--radius-1)", padding: "var(--space-3)" }}>
        <legend>Channels</legend>
        {channels.length === 0 ? (
          <span style={{ color: "var(--color-text-muted)" }}>No channels — rules cannot notify without one.</span>
        ) : (
          channels.map((c) => (
            <label key={c.id} style={{ display: "flex", gap: "var(--space-2)", alignItems: "center" }}>
              <input type="checkbox" checked={r.channels.includes(c.id)} onChange={() => toggleChannel(c.id)} />
              {c.name} <span style={{ color: "var(--color-text-muted)" }}>({c.kind})</span>
            </label>
          ))
        )}
      </fieldset>

      <fieldset style={{ border: "none", padding: 0 }}>
        <legend className="p-field-label">Enabled</legend>
        <label style={{ marginRight: "var(--space-3)" }}>
          <input type="radio" name="enabled" checked={r.enabled} onChange={() => setR({ ...r, enabled: true })} /> on
        </label>
        <label>
          <input type="radio" name="enabled" checked={!r.enabled} onChange={() => setR({ ...r, enabled: false })} /> off
        </label>
      </fieldset>

      {errorMessage ? (
        <p role="alert" style={{ color: "var(--status-error)" }}>
          {errorMessage}
        </p>
      ) : null}
      {testMessage ? <p role="status">{testMessage}</p> : null}

      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        {onTest ? (
          <Button type="button" onClick={() => onTest(r)} loading={testing}>
            Test rule
          </Button>
        ) : null}
        <Button type="submit" variant="primary" loading={saving} disabled={!r.name}>
          Save
        </Button>
      </div>
    </form>
  );
}
