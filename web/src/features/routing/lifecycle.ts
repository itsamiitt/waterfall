// features/routing/lifecycle.ts — PURE config-lifecycle logic (doc 07 §6), unit-tested with no
// DOM. The client MIRRORS server validation; the SERVER validator is authority, so the Publish
// gate here is a UX pre-check — the button is disabled until a server validate returns ok, and
// any edit after validate demotes validated→draft exactly as the server does (doc 04 §2.7).
import type { ConfigVersionStatus } from "../../lib/status";
import type { EffectiveOverride, TriMode, ValidationReport } from "./types";

export interface PublishGate {
  /** validate is runnable on any mutable version (draft or validated). */
  canValidate: boolean;
  /** Publish is enabled ONLY when the server has validated the exact current bytes. */
  canPublish: boolean;
  /** Why publish is blocked, surfaced beside the disabled button (null when enabled). */
  reason: string | null;
}

/**
 * The publish-gated-on-validate state machine.
 * @param status         server version status (draft|validated|published|archived)
 * @param dirtySinceValidate  client tracked: any PATCH after a passing validate (reverts to draft)
 * @param errorCount     number of error-severity entries in the stored validation_report
 */
export function publishGate(
  status: ConfigVersionStatus,
  dirtySinceValidate: boolean,
  errorCount: number,
): PublishGate {
  if (status === "published" || status === "archived") {
    return { canValidate: false, canPublish: false, reason: "this version is already published" };
  }
  // A local edit after validate reverts the version to draft (server + client mirror).
  if (status === "validated" && dirtySinceValidate) {
    return {
      canValidate: true,
      canPublish: false,
      reason: "draft changed since validate — re-validate before publishing",
    };
  }
  if (status === "validated") {
    if (errorCount > 0) {
      // Defensive: the server never marks a version validated with errors.
      return { canValidate: true, canPublish: false, reason: "validation reported errors" };
    }
    return { canValidate: true, canPublish: true, reason: null };
  }
  // draft
  return { canValidate: true, canPublish: false, reason: "run validate — server must pass first" };
}

/** The effective badge status token for a validation report (doc 07 §5). */
export function reportSeverity(report: ValidationReport | null | undefined): "ok" | "warn" | "error" {
  if (!report) return "ok";
  if (report.errors.length > 0) return "error";
  if (report.warnings.length > 0) return "warn";
  return "ok";
}

// ---- tri-state resolver display (doc 07 §3.2 / §3.3) ----

const TRI_LABEL: Record<TriMode, string> = {
  inherit: "Inherit",
  off: "Off",
  on: "On",
};

export function triLabel(mode: TriMode): string {
  return TRI_LABEL[mode] ?? mode;
}

/**
 * Render the RESOLVED effective value + its source scope, verbatim from the resolver output —
 * never re-derived here (doc 07 §3.2: "the model proposes, a deterministic gate disposes").
 * Example: {effective:"off", source:"tenant default", source_version:7} →
 *   "off — inherited from tenant default, v7".
 */
export function describeEffective(o: EffectiveOverride): string {
  const v = typeof o.effective === "string" ? o.effective : triLabel(o.effective);
  const src = o.source === "engine_default" ? "engine default" : o.source;
  const ver = o.source_version !== undefined ? `, v${o.source_version}` : "";
  const verb = o.source === "engine_default" ? "from" : "inherited from";
  return `${v} — ${verb} ${src}${ver}`;
}

/** Status token for the effective chip: on=ok, off=neutral, inherit/other=info. */
export function effectiveToken(o: EffectiveOverride): "ok" | "neutral" | "info" {
  if (o.effective === "on") return "ok";
  if (o.effective === "off") return "neutral";
  return "info";
}

// ---- pure reorder helper (drives the dnd-kit onDragEnd; testable without a DOM) ----

/** Move `activeId` to the slot occupied by `overId`, preserving all other order. */
export function moveItem<T extends string>(list: readonly T[], activeId: T, overId: T): T[] {
  const from = list.indexOf(activeId);
  const to = list.indexOf(overId);
  if (from === -1 || to === -1 || from === to) return [...list];
  const next = [...list];
  next.splice(from, 1);
  next.splice(to, 0, activeId);
  return next;
}
