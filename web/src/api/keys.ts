// api/keys.ts — the hierarchical query-key factory (doc 08 §2, OI-UI-2). The staleTime tier
// is declared HERE beside the key, never ad hoc at call sites (doc 08 §4): config 30s,
// telemetry 5s. The SSE manager patches these exact keys (§5), so every feature MUST source
// keys from this file.

import type { QueryClient } from "@tanstack/react-query";
import type { SseTopic } from "./types";

export const staleTimes = {
  /** Providers, Provider Keys metadata, Key Pools, users, rules, versions, budgets. */
  config: 30_000,
  /** Stats, health, queue/worker snapshots, cost, overview snapshot. */
  telemetry: 5_000,
} as const;

export const qk = {
  auth: {
    me: ["auth", "me"] as const,
    roles: ["auth", "roles"] as const,
    sessions: ["auth", "sessions"] as const,
  },
  overview: {
    snapshot: ["overview", "snapshot"] as const,
    tile: (tile: string) => ["overview", "tile", tile] as const,
  },
  search: (q: string) => ["search", q] as const,
  providers: {
    root: ["providers"] as const,
    list: (filters?: Record<string, unknown>) => ["providers", "list", filters ?? {}] as const,
    detail: (id: string) => ["providers", "detail", id] as const,
    keys: (id: string) => ["providers", "detail", id, "keys"] as const,
    health: (id: string) => ["providers", "detail", id, "health"] as const,
    stats: (id: string, win?: string) => ["providers", "detail", id, "stats", win ?? ""] as const,
  },
  keys: {
    root: ["keys"] as const,
    detail: (id: string) => ["keys", "detail", id] as const,
  },
  pools: {
    root: ["pools"] as const,
    detail: (id: string) => ["pools", "detail", id] as const,
  },
  health: {
    root: ["health"] as const,
  },
  queues: {
    root: ["queues"] as const,
    snapshot: ["queues", "snapshot"] as const,
    stats: (name: string, win?: string) => ["queues", "stats", name, win ?? ""] as const,
  },
  workers: {
    root: ["workers"] as const,
    detail: (id: string) => ["workers", "detail", id] as const,
  },
  alerts: {
    root: ["alerts"] as const,
    events: ["alerts", "events"] as const,
  },
  imports: {
    root: ["imports"] as const,
    detail: (jobId: string) => ["imports", "detail", jobId] as const,
  },
  approvals: {
    root: ["approvals"] as const,
    detail: (id: string) => ["approvals", "detail", id] as const,
  },
  users: {
    root: ["users"] as const,
    detail: (id: string) => ["users", "detail", id] as const,
  },
} as const;

/** Root query key invalidated when a topic's `*.changed`-class event arrives (doc 04 §3.4:
 * invalidate and refetch authoritative state — never merge payloads). */
export function topicInvalidationRoots(topic: SseTopic): readonly (readonly string[])[] {
  switch (topic) {
    case "overview":
      return [qk.overview.snapshot];
    case "provider":
      return [qk.providers.root, qk.health.root];
    case "key":
      return [qk.keys.root, qk.providers.root, qk.pools.root];
    case "queue":
      return [qk.queues.root];
    case "worker":
      return [qk.workers.root];
    case "alert":
      return [qk.alerts.root];
    case "import":
      return [qk.imports.root];
    case "approval":
      return [qk.approvals.root];
  }
}

/** Reset / degraded-poll refetch: invalidate a topic's snapshot queries (doc 04 §3.5). */
export function invalidateTopic(qc: QueryClient, topic: SseTopic): void {
  for (const key of topicInvalidationRoots(topic)) {
    void qc.invalidateQueries({ queryKey: key as unknown as string[] });
  }
}
