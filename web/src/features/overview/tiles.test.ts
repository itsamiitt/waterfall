// Overview tile -> endpoint/route map (P11 acceptance: doc 09 §1.2 normative 19-tile vocabulary).
// The drill-down deep link of every tile must point at the module doc 09 §1.2 assigns it; the
// single non-navigating tile (system_health) must carry no href.
import { describe, expect, it } from "vitest";
import { TILE_ORDER, orderedTiles } from "./tiles";
import type { TileValue } from "../../api/types";

/** doc 09 §1.2 "Tile → endpoint + SSE map": tile id -> the module route the tile drills into. */
const NORMATIVE: Record<string, string | null> = {
  providers_summary: "/providers",
  provider_health_split: "/health",
  keys_summary: "/keys",
  credits_remaining: "/providers",
  requests_today: "/cost",
  rps_now: "/health",
  jobs_summary: "/queues",
  retry_depth: "/queues",
  dlq_depth: "/dead-letters",
  worker_health: "/workers",
  queue_health: "/queues",
  success_failure_rate: "/health",
  avg_cost_per_result: "/cost",
  avg_response_ms: "/health",
  provider_ranking: "/providers/compare",
  coverage: "/providers/compare",
  cost_today: "/cost",
  cost_month: "/cost",
  system_health: null,
};

describe("overview tile -> endpoint map (doc 09 §1.2)", () => {
  const byId = new Map(TILE_ORDER.map((t) => [t.id, t]));

  it("defines all 19 normative tiles", () => {
    for (const id of Object.keys(NORMATIVE)) {
      expect(byId.has(id), `tile ${id} missing from TILE_ORDER`).toBe(true);
    }
  });

  it("each tile deep-links to its doc 09 §1.2 module (system_health does not navigate)", () => {
    for (const [id, href] of Object.entries(NORMATIVE)) {
      const meta = byId.get(id);
      expect(meta, `tile ${id}`).toBeDefined();
      if (href === null) expect(meta!.href, `${id} must not navigate`).toBeUndefined();
      else expect(meta!.href, `${id} drill-down`).toBe(href);
    }
  });

  it("orders snapshot tiles by the vocabulary and keeps unknown (additive) tiles rather than dropping them", () => {
    const tiles: Record<string, TileValue> = {
      cost_today: { value: 88410, budget_pct: 44 },
      providers_summary: { value: 87, of: 112 },
      some_future_tile: { value: 1 },
    };
    const ordered = orderedTiles(tiles);
    const ids = ordered.map((o) => o.meta.id);
    // providers_summary precedes cost_today (vocabulary order), unknown tile lands at the end.
    expect(ids.indexOf("providers_summary")).toBeLessThan(ids.indexOf("cost_today"));
    expect(ids).toContain("some_future_tile");
  });
});
