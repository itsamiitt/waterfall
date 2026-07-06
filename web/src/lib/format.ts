// lib/format.ts — display formatting. Dates are UTC everywhere (doc 04 §1.1); dates use
// Intl/Date only (ADR-0016: no date library). Derived values are NEVER computed here — the
// API sends them; these helpers only render.

/** "2026-07-02T15:04:05Z" | Date -> "2026-07-02 15:04:05Z" (always UTC). */
export function formatUtc(ts: string | Date): string {
  const d = typeof ts === "string" ? new Date(ts) : ts;
  if (Number.isNaN(d.getTime())) return "—";
  return d.toISOString().replace("T", " ").replace(/\.\d{3}Z$/, "Z");
}

/** Short UTC time-of-day: "12:40:02Z". */
export function formatUtcTime(ts: string | Date): string {
  const d = typeof ts === "string" ? new Date(ts) : ts;
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.toISOString().slice(11, 19)}Z`;
}

/** Relative age: "41s ago", "5m ago", "2h ago", "3d ago". Future or invalid -> "—". */
export function relativeTime(ts: string | Date, now: Date = new Date()): string {
  const d = typeof ts === "string" ? new Date(ts) : ts;
  const ms = now.getTime() - d.getTime();
  if (Number.isNaN(ms) || ms < 0) return "—";
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

const intFmt = new Intl.NumberFormat("en-US");

/** Credits and other counts: "88,410". */
export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "—";
  return intFmt.format(n);
}

/** Abbreviated tile numbers: 1284031 -> "1.28M", 14900 -> "14.9K". */
export function formatCompact(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "—";
  const abs = Math.abs(n);
  if (abs >= 1_000_000) return `${trimZeros((n / 1_000_000).toFixed(2))}M`;
  if (abs >= 10_000) return `${trimZeros((n / 1_000).toFixed(1))}K`;
  return intFmt.format(n);
}

/** Credits: "88,410 credits" (or bare number when unit is rendered separately). */
export function formatCredits(n: number | null | undefined, withUnit = false): string {
  const s = formatCount(n);
  return withUnit && s !== "—" ? `${s} credits` : s;
}

/** Fraction 0..1 -> "94.3%"; pass {fromPercent: true} for values already in 0..100. */
export function formatPercent(
  x: number | null | undefined,
  opts?: { fromPercent?: boolean; digits?: number },
): string {
  if (x === null || x === undefined || Number.isNaN(x)) return "—";
  const pct = opts?.fromPercent ? x : x * 100;
  return `${trimZeros(pct.toFixed(opts?.digits ?? 1))}%`;
}

/** Signed delta percentage (already in percent units): 4.2 -> "+4.2%". */
export function formatDeltaPct(x: number | null | undefined): string {
  if (x === null || x === undefined || Number.isNaN(x)) return "—";
  const sign = x > 0 ? "+" : "";
  return `${sign}${trimZeros(x.toFixed(1))}%`;
}

/** Latency in ms: 412 -> "412ms", 1840 -> "1.84s". */
export function formatLatencyMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${trimZeros((ms / 1000).toFixed(2))}s`;
}

/** Duration in seconds: 41 -> "41s", 341 -> "5m 41s", 7261 -> "2h 1m". */
export function formatDurationS(s: number | null | undefined): string {
  if (s === null || s === undefined || Number.isNaN(s) || s < 0) return "—";
  const sec = Math.round(s);
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) {
    const rs = sec % 60;
    return rs ? `${m}m ${rs}s` : `${m}m`;
  }
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm ? `${h}h ${rm}m` : `${h}h`;
}

function trimZeros(s: string): string {
  return s.includes(".") ? s.replace(/\.?0+$/, "") : s;
}
