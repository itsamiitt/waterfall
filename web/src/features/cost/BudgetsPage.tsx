// features/cost — Budgets (doc 09 §10.1, doc 04 §2.10). Progress meters with tick marks at each
// alert_pct; UTC period semantics. Doctrine in copy: budgets ALERT, G4 cost ceilings ENFORCE.
// PUT is a full replacement of the Tenant's budget set (budgets.write gated).
import { useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Badge, Button, EmptyState, Input } from "../../design/primitives";
import { formatCount } from "../../lib/format";
import { useBudgets, useUpdateBudgets } from "./api";
import type { BudgetItem } from "./types";

function pct(consumed: number, limit: number): number {
  if (!limit) return 0;
  return Math.min(999, Math.round((consumed / limit) * 100));
}

function meterToken(p: number): "ok" | "warn" | "error" {
  if (p >= 100) return "error";
  if (p >= 80) return "warn";
  return "ok";
}

function Meter({ b }: { b: BudgetItem }) {
  const consumed = b.consumed_credits ?? 0;
  const p = pct(consumed, b.limit_credits);
  const token = meterToken(p);
  const fillColor =
    token === "error" ? "var(--status-error)" : token === "warn" ? "var(--status-warn)" : "var(--status-ok)";
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-2)" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
        <strong>
          {b.scope}:{b.scope_key} <span style={{ color: "var(--color-text-muted)" }}>· {b.period}</span>
        </strong>
        <Badge status={token} icon={token === "ok" ? "check" : "triangle"} label={`${p}% of ${formatCount(b.limit_credits)}`} />
      </div>
      <div
        role="meter"
        aria-valuemin={0}
        aria-valuemax={b.limit_credits}
        aria-valuenow={consumed}
        aria-label={`${b.scope} ${b.scope_key} ${b.period} budget: ${formatCount(consumed)} of ${formatCount(b.limit_credits)} credits (${p}%)`}
        style={{
          position: "relative",
          height: 14,
          background: "var(--color-bg-sunken)",
          border: "1px solid var(--color-border)",
          borderRadius: "var(--radius-1)",
          overflow: "hidden",
        }}
      >
        <div style={{ width: `${Math.min(100, p)}%`, height: "100%", background: fillColor }} />
        {b.alert_pct.map((t) => (
          <span
            key={t}
            title={`alert at ${t}%`}
            style={{
              position: "absolute",
              top: -3,
              bottom: -3,
              left: `${Math.min(100, t)}%`,
              width: 2,
              background: "var(--color-text)",
            }}
            aria-hidden="true"
          />
        ))}
      </div>
      <span style={{ fontSize: "var(--text-xs)", color: "var(--color-text-muted)" }}>
        {formatCount(consumed)} / {formatCount(b.limit_credits)} credits · alerts at {b.alert_pct.join("/")}%
        {b.current_period_start ? ` · period from ${b.current_period_start}` : ""}
      </span>
    </div>
  );
}

export default function BudgetsPage() {
  const budgets = useBudgets();
  const update = useUpdateBudgets();
  const [draft, setDraft] = useState<BudgetItem[] | null>(null);

  const items = budgets.data?.items ?? [];

  function startEdit() {
    setDraft(items.map((b) => ({ ...b, alert_pct: [...b.alert_pct] })));
  }

  function save() {
    if (!draft) return;
    update.mutate(draft, {
      onSuccess: () => {
        toast.success("Budgets replaced");
        setDraft(null);
      },
      onError: (e) => toast.error(isApiError(e) ? `Save failed (${e.code})` : "Save failed"),
    });
  }

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Budgets</h1>
        <span className="page-header-meta">budgets alert · G4 cost ceilings enforce</span>
      </div>

      {budgets.isError ? (
        <EmptyState
          variant="error"
          title="Could not load budgets"
          errorCode={isApiError(budgets.error) ? budgets.error.code : undefined}
          action={{ label: "Retry", onClick: () => void budgets.refetch() }}
        />
      ) : budgets.isPending ? (
        <div className="skeleton" style={{ height: 240 }} aria-busy="true" aria-label="Loading budgets" />
      ) : items.length === 0 && !draft ? (
        <EmptyState
          variant="zero-data"
          title="No budgets set"
          body="Define per-Tenant or per-Provider budgets to receive threshold alerts."
          action={{ label: "Add a budget", onClick: startEdit }}
        />
      ) : draft ? (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-4)", maxWidth: 640 }}>
          {draft.map((b, i) => (
            <div key={`${b.scope}:${b.scope_key}:${b.period}`} style={{ display: "flex", gap: "var(--space-3)", flexWrap: "wrap" }}>
              <Input
                label={`${b.scope}:${b.scope_key} (${b.period}) limit`}
                value={String(b.limit_credits)}
                inputMode="numeric"
                onChange={(v) =>
                  setDraft((d) => d!.map((x, j) => (j === i ? { ...x, limit_credits: Number(v) || 0 } : x)))
                }
              />
              <Input
                label="alert_pct (comma-separated)"
                value={b.alert_pct.join(",")}
                onChange={(v) =>
                  setDraft((d) =>
                    d!.map((x, j) =>
                      j === i
                        ? { ...x, alert_pct: v.split(",").map((n) => Number(n.trim())).filter((n) => !Number.isNaN(n)) }
                        : x,
                    ),
                  )
                }
              />
            </div>
          ))}
          <div style={{ display: "flex", gap: "var(--space-2)" }}>
            <Button variant="primary" onClick={save} loading={update.isPending}>
              Save (full replacement)
            </Button>
            <Button onClick={() => setDraft(null)} disabled={update.isPending}>
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--space-5)", maxWidth: 640 }}>
          <div>
            <Button size="sm" onClick={startEdit}>
              Edit budgets
            </Button>
          </div>
          {items.map((b) => (
            <Meter key={`${b.scope}:${b.scope_key}:${b.period}`} b={b} />
          ))}
        </div>
      )}
    </>
  );
}
