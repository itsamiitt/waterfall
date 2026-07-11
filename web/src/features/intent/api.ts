// features/intent/api.ts — the ONLY place intent endpoint paths are named (doc 08 §2).
// Screen → endpoint map (docs/research-intelligence/08):
//   IntentPage list          GET /intent/accounts
//   IntentAccountPage detail GET /intent/accounts/{domain}
import { useQuery } from "@tanstack/react-query";
import { get } from "../../api/client";
import { staleTimes } from "../../api/keys";
import type { AccountResponse, AccountsResponse } from "./types";

/** Feature-local query keys (mirrors the api/keys.ts convention without editing shared state). */
export const ik = {
  accounts: ["intent", "accounts"] as const,
  account: (domain: string) => ["intent", "account", domain] as const,
};

/** GET /intent/accounts — the Tenant's accounts with computed intent, strongest first. */
export function useIntentAccounts() {
  return useQuery({
    queryKey: ik.accounts,
    queryFn: () => get<AccountsResponse>("/intent/accounts"),
    staleTime: staleTimes.telemetry,
  });
}

/** GET /intent/accounts/{domain} — the per-class breakdown for one account. */
export function useIntentAccount(domain: string) {
  return useQuery({
    queryKey: ik.account(domain),
    queryFn: () => get<AccountResponse>(`/intent/accounts/${encodeURIComponent(domain)}`),
    staleTime: staleTimes.telemetry,
    enabled: domain !== "",
  });
}
