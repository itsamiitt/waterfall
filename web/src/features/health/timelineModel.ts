// features/health/timelineModel.ts — pure model for the 90-day uptime strip + 48h heatmap
// (doc 09 §5.1). Kept separate from the SVG so the segment→color mapping and no_data handling
// are unit-testable. CRITICAL (doc 09 §5.3): `no_data` is a DISTINCT visual from `up` — a
// provider with no checks yet is not "healthy", it is unknown.
import type { StatusToken } from "../../lib/status";

export type HealthState = "up" | "degraded" | "down" | "no_data" | string;

export interface DaySegment {
  date: string;
  status: HealthState;
  uptime_pct?: number;
  worst_error_class?: string | null;
  check_count?: number;
}

export interface HourBucket {
  ts: string;
  status?: HealthState;
  success_rate?: number;
  check_count?: number;
}

export interface SegmentStyle {
  token: StatusToken;
  /** true → render with the distinct "no data" treatment (hatched/empty), never as up. */
  noData: boolean;
  label: string;
}

const STYLES: Record<string, SegmentStyle> = {
  up: { token: "ok", noData: false, label: "up" },
  degraded: { token: "warn", noData: false, label: "degraded" },
  down: { token: "error", noData: false, label: "down" },
  no_data: { token: "neutral", noData: true, label: "no data" },
};

/** Map a health state to its color token + no_data flag. Unknown states degrade to no_data
 * treatment rather than being mistaken for up. */
export function segmentStyle(status: HealthState): SegmentStyle {
  return STYLES[status] ?? { token: "neutral", noData: true, label: String(status) };
}

/** Ensure exactly `count` day-segments; missing leading days pad as no_data (a provider with a
 * short history is not silently shown as fully up). */
export function padDays(days: readonly DaySegment[], count = 90): DaySegment[] {
  if (days.length >= count) return days.slice(days.length - count);
  const pad: DaySegment[] = [];
  const needed = count - days.length;
  for (let i = 0; i < needed; i++) pad.push({ date: "", status: "no_data" });
  return [...pad, ...days];
}

/** Per-segment tooltip (doc 09 §5.2): status, uptime_pct, worst_error_class, check_count. */
export function segmentTitle(seg: DaySegment): string {
  const parts = [seg.date || "no date", segmentStyle(seg.status).label];
  if (seg.uptime_pct !== undefined) parts.push(`${seg.uptime_pct}% uptime`);
  if (seg.worst_error_class) parts.push(`worst: ${seg.worst_error_class}`);
  if (seg.check_count !== undefined) parts.push(`${seg.check_count} checks`);
  return parts.join(" · ");
}

/** Overall uptime % across the segments that actually have data (no_data excluded). */
export function overallUptimePct(days: readonly DaySegment[]): number | null {
  const withData = days.filter((d) => d.status !== "no_data" && d.uptime_pct !== undefined);
  if (withData.length === 0) return null;
  const sum = withData.reduce((a, d) => a + (d.uptime_pct ?? 0), 0);
  return Math.round((sum / withData.length) * 10) / 10;
}
