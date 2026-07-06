// overview/api.ts — the ONLY place overview endpoint paths are named (doc 08 §2).
import { useQuery } from "@tanstack/react-query";
import { get } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import type { OverviewSnapshot } from "../../api/types";

/** GET /v1/admin/overview — full tile snapshot served from the aggregator's last 2s tick
 * (doc 04 §2.13). Live updates arrive as overview.tiles.tick via the SSE manager, which
 * setQueryData-replaces this exact key. */
export function useOverview() {
  return useQuery({
    queryKey: qk.overview.snapshot,
    queryFn: () => get<OverviewSnapshot>("/overview"),
    staleTime: staleTimes.telemetry,
  });
}
