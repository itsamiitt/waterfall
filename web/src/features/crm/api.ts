// features/crm/api.ts — the ONLY place the CRM connections endpoint path is named (doc 08 §2).
//   CRMPage  GET /crm/connections
import { useQuery } from "@tanstack/react-query";
import { get } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type { ConnectionsResponse } from "./types";

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const ck = {
  connections: ["crm", "connections"] as const,
};

/** GET /crm/connections — the Tenant's configured CRM outbound connections. */
export function useCRMConnections() {
  return useQuery({
    queryKey: ck.connections,
    queryFn: () => get<ConnectionsResponse>("/crm/connections"),
    staleTime: staleTimes.config,
  });
}
