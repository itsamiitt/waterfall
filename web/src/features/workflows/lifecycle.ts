// features/workflows/lifecycle.ts — PURE config-lifecycle + canvas logic (doc 07 §4/§6),
// unit-tested with no DOM. The Publish gate mirrors the server (button disabled until validate
// passes); the canvas model turns the flat payload arrays into stepped lanes and back.
import type { ConfigVersionStatus } from "../../lib/status";
import type { DryRunResult, ValidationReport, WaterfallWorkflowPayload } from "./types";

export interface PublishGate {
  canValidate: boolean;
  canPublish: boolean;
  reason: string | null;
}

/** Publish-gated-on-validate state machine (shared shape with routing; doc 07 §6). */
export function publishGate(
  status: ConfigVersionStatus,
  dirtySinceValidate: boolean,
  errorCount: number,
): PublishGate {
  if (status === "published" || status === "archived") {
    return { canValidate: false, canPublish: false, reason: "this version is already published" };
  }
  if (status === "validated" && dirtySinceValidate) {
    return { canValidate: true, canPublish: false, reason: "canvas changed since validate — re-validate first" };
  }
  if (status === "validated") {
    if (errorCount > 0) return { canValidate: true, canPublish: false, reason: "validation reported errors" };
    return { canValidate: true, canPublish: true, reason: null };
  }
  return { canValidate: true, canPublish: false, reason: "run validate — server must pass first" };
}

export function reportSeverity(report: ValidationReport | null | undefined): "ok" | "warn" | "error" {
  if (!report) return "ok";
  if (report.errors.length > 0) return "error";
  if (report.warnings.length > 0) return "warn";
  return "ok";
}

// ---- stepped-canvas model (entry → parallel → sequential → fallback, doc 09 §7.1) ----

export type StepKind = "entry" | "parallel" | "sequential" | "fallback";

export interface CanvasNode {
  /** Stable dnd id, unique across the canvas. */
  id: string;
  step: StepKind;
  provider: string;
}

/** Derive the stepped canvas nodes from a payload (entry, parallel[], sequential[], fallback). */
export function canvasNodes(p: WaterfallWorkflowPayload): CanvasNode[] {
  const nodes: CanvasNode[] = [];
  if (p.entry_provider) nodes.push({ id: `entry:${p.entry_provider}`, step: "entry", provider: p.entry_provider });
  for (const pr of p.parallel_providers ?? []) nodes.push({ id: `parallel:${pr}`, step: "parallel", provider: pr });
  for (const pr of p.sequential_providers ?? []) nodes.push({ id: `sequential:${pr}`, step: "sequential", provider: pr });
  if (p.fallback_provider) nodes.push({ id: `fallback:${p.fallback_provider}`, step: "fallback", provider: p.fallback_provider });
  return nodes;
}

/** Providers referenced anywhere in the canvas (for the node-inspector picker + dedupe). */
export function canvasProviders(p: WaterfallWorkflowPayload): string[] {
  return canvasNodes(p).map((n) => n.provider);
}

/** Reorder within the sequential step (dnd), preserving other steps. */
export function reorderSequential(
  p: WaterfallWorkflowPayload,
  activeProvider: string,
  overProvider: string,
): WaterfallWorkflowPayload {
  const seq = [...(p.sequential_providers ?? [])];
  const from = seq.indexOf(activeProvider);
  const to = seq.indexOf(overProvider);
  if (from === -1 || to === -1 || from === to) return p;
  seq.splice(from, 1);
  seq.splice(to, 0, activeProvider);
  return { ...p, sequential_providers: seq };
}

/** Validation entries anchored to a canvas node path (e.g. VR-2 excluded Provider on a node). */
export function nodeErrorPaths(report: ValidationReport | null | undefined): Map<string, string> {
  const map = new Map<string, string>();
  if (!report) return map;
  for (const e of report.errors) map.set(e.path, e.message);
  return map;
}

/** Guard: dry-run must assert zero egress (backend guarantee, doc 07 §7). */
export function dryRunZeroEgress(r: DryRunResult): boolean {
  return r.zero_egress === true;
}
