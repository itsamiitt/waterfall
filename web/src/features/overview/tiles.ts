// The 19-tile vocabulary of doc 09 §1.2 (normative; OI-WF-2): label + drill-down deep link.
// Values render verbatim from the snapshot — nothing is derived client-side (doc 08 §4).
import type { TileValue } from "../../api/types";
import {
  formatCompact,
  formatCount,
  formatDurationS,
  formatPercent,
} from "../../lib/format";

export interface TileMeta {
  id: string;
  label: string;
  /** Drill-down route with the tile's filter pre-applied (doc 09 §1.2/§1.3); the single
   * non-navigating tile (system_health) has none. */
  href?: string;
  /** Primary value renderer; missing data renders an em-dash ("no data yet"). */
  render: (v: TileValue) => string;
  unit?: string;
  sub?: (v: TileValue) => string | undefined;
}

const dash = "—";

export const TILE_ORDER: TileMeta[] = [
  {
    id: "providers_summary",
    label: "Providers",
    href: "/providers",
    render: (v) => formatCount(v.value),
    sub: (v) => (v.of !== undefined ? `of ${formatCount(v.of)} total` : undefined),
  },
  {
    id: "provider_health_split",
    label: "Provider health",
    href: "/health",
    render: (v) => formatCount(v.value),
    sub: () => "healthy",
  },
  {
    id: "keys_summary",
    label: "Provider Keys",
    href: "/keys",
    render: (v) => formatCount(v.value),
    sub: (v) => (v.of !== undefined ? `of ${formatCount(v.of)} total` : undefined),
  },
  { id: "keys_degraded", label: "Keys degraded", href: "/keys", render: (v) => formatCount(v.value) },
  {
    id: "credits_remaining",
    label: "Credits remaining",
    href: "/providers",
    render: (v) => formatCompact(v.value),
    unit: "credits",
  },
  {
    id: "requests_today",
    label: "Requests today",
    href: "/cost",
    render: (v) => formatCompact(v.value),
  },
  {
    id: "enrichments_24h",
    label: "Enrichments (24h)",
    href: "/cost",
    render: (v) => formatCompact(v.value),
  },
  { id: "rps_now", label: "Requests/s", href: "/health", render: (v) => formatCount(v.value) },
  {
    id: "jobs_summary",
    label: "Enrichment Jobs",
    href: "/queues",
    render: (v) => formatCount(v.value),
    sub: () => "running",
  },
  { id: "retry_depth", label: "Retry depth", href: "/queues", render: (v) => formatCount(v.value) },
  { id: "dlq_depth", label: "Dead letters", href: "/dead-letters", render: (v) => formatCount(v.value) },
  {
    id: "worker_health",
    label: "Workers",
    href: "/workers",
    render: (v) => formatCount(v.value),
    sub: () => "running",
  },
  { id: "workers_lost", label: "Workers lost", href: "/workers", render: (v) => formatCount(v.value) },
  {
    id: "queue_health",
    label: "Worst queue age",
    href: "/queues",
    render: (v) => (v.value_s !== undefined ? formatDurationS(v.value_s) : dash),
    sub: (v) => (typeof v.queue === "string" ? `queue: ${v.queue}` : undefined),
  },
  {
    id: "success_failure_rate",
    label: "Success rate (1h)",
    href: "/health",
    render: (v) => formatPercent(v.value),
  },
  {
    id: "success_rate_1h",
    label: "Success rate (1h)",
    href: "/health",
    render: (v) => formatPercent(v.value),
  },
  {
    id: "avg_cost_per_result",
    label: "Cost per result",
    href: "/cost",
    render: (v) => (v.value !== undefined ? String(v.value) : dash),
    unit: "credits",
  },
  {
    id: "avg_response_ms",
    label: "Latency p50",
    href: "/health",
    render: (v) => (v.value !== undefined ? `${formatCount(v.value)}ms` : dash),
  },
  {
    id: "provider_ranking",
    label: "Top provider",
    href: "/providers/compare",
    render: (v) => (typeof v.provider === "string" ? v.provider : dash),
  },
  {
    id: "coverage",
    label: "Coverage",
    href: "/providers/compare",
    render: (v) => formatPercent(v.value),
  },
  {
    id: "active_providers",
    label: "Active providers",
    href: "/providers",
    render: (v) => formatCount(v.value),
    sub: (v) => (v.of !== undefined ? `of ${formatCount(v.of)}` : undefined),
  },
  {
    id: "worst_queue_oldest_age",
    label: "Worst queue age",
    href: "/queues",
    render: (v) => (v.value_s !== undefined ? formatDurationS(v.value_s) : dash),
    sub: (v) => (typeof v.queue === "string" ? `queue: ${v.queue}` : undefined),
  },
  {
    id: "cost_today",
    label: "Credits today",
    href: "/cost",
    render: (v) => formatCompact(v.value),
    sub: (v) => (v.budget_pct !== undefined ? `budget ${v.budget_pct}%` : undefined),
  },
  {
    id: "credits_today",
    label: "Credits today",
    href: "/cost",
    render: (v) => formatCompact(v.value),
    sub: (v) => (v.budget_pct !== undefined ? `budget ${v.budget_pct}%` : undefined),
  },
  {
    id: "cost_month",
    label: "Credits this month",
    href: "/cost",
    render: (v) => formatCompact(v.value),
    sub: () => "modeled",
  },
  {
    id: "open_alerts",
    label: "Open alerts",
    href: "/alerts",
    render: (v) => formatCount((v.critical ?? 0) + (v.warning ?? 0)),
    sub: (v) => `${formatCount(v.critical ?? 0)} critical, ${formatCount(v.warning ?? 0)} warning`,
  },
  {
    id: "system_health",
    label: "System health",
    // deliberately no drill-down (doc 09 §1.2: the single non-navigating tile)
    render: (v) => (v.value !== undefined ? formatCount(v.value) : "OK"),
  },
];

const KNOWN = new Map(TILE_ORDER.map((t) => [t.id, t]));

/** Order the snapshot's tiles by the doc 09 vocabulary; unknown tiles (additive server
 * change) render generically at the end rather than being dropped. */
export function orderedTiles(tiles: Record<string, TileValue>): { meta: TileMeta; value: TileValue }[] {
  const out: { meta: TileMeta; value: TileValue }[] = [];
  for (const meta of TILE_ORDER) {
    const value = tiles[meta.id];
    if (value !== undefined) out.push({ meta, value });
  }
  for (const [id, value] of Object.entries(tiles)) {
    if (!KNOWN.has(id)) {
      out.push({
        meta: { id, label: id.replaceAll("_", " "), render: (v) => (v.value !== undefined ? String(v.value) : dash) },
        value,
      });
    }
  }
  return out;
}
