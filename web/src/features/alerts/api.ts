// features/alerts/api.ts — the ONLY place alert endpoint paths are named (doc 08 §2).
// Screen → endpoint map (doc 09 §14 rows 30-32):
//   RuleEditor / rules list  GET/POST /alerts/rules, GET/PATCH/DELETE /alerts/rules/{id}, POST …/test
//   ChannelsPanel            GET/POST/DELETE /alerts/channels[/{id}], POST /alerts/channels/{id}/test
//   EventsFeed               GET /alerts/events, POST /alerts/events/{id}/ack
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, patch, post } from "../../api/client";
import { qk, staleTimes } from "../../api/keys";
import { listQuery } from "../../lib/cursors";
import type {
  AlertChannel,
  AlertRule,
  AlertRuleInput,
  ChannelTestResult,
  ChannelsResponse,
  EventsResponse,
  RuleTestResult,
  RulesResponse,
} from "./types";
import type { ChannelKind } from "./vocab";

export const ak = {
  rules: (q: string) => ["alerts", "rules", q] as const,
  rule: (id: string) => ["alerts", "rules", "detail", id] as const,
  channels: ["alerts", "channels"] as const,
  events: qk.alerts.events,
};

export interface RuleFilters {
  metric?: string;
  severity?: string;
  enabled?: string;
}

export function useRules(filters: RuleFilters) {
  const query = listQuery({ ...filters });
  return useQuery({
    queryKey: ak.rules(query),
    queryFn: () => get<RulesResponse>(`/alerts/rules${query}`),
    staleTime: staleTimes.config,
  });
}

export function useRule(id: string | undefined) {
  return useQuery({
    queryKey: ak.rule(id ?? ""),
    queryFn: () => get<AlertRule>(`/alerts/rules/${id}`),
    enabled: !!id,
    staleTime: staleTimes.config,
  });
}

export function useCreateRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AlertRuleInput) => post<AlertRule>("/alerts/rules", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.alerts.root }),
  });
}

export function useUpdateRule(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<AlertRuleInput> & { muted_until?: string | null }) =>
      patch<AlertRule>(`/alerts/rules/${id}`, body),
    onSuccess: (r) => {
      qc.setQueryData(ak.rule(id), r);
      void qc.invalidateQueries({ queryKey: qk.alerts.root });
    },
  });
}

export function useDeleteRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => del<void>(`/alerts/rules/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.alerts.root }),
  });
}

/** POST /alerts/rules/{id}/test — evaluate now; no notification sent (doc 04 §2.11). */
export function useTestRule(id: string) {
  return useMutation({
    mutationFn: () => post<RuleTestResult>(`/alerts/rules/${id}/test`),
  });
}

export function useChannels() {
  return useQuery({
    queryKey: ak.channels,
    queryFn: () => get<ChannelsResponse>("/alerts/channels"),
    staleTime: staleTimes.config,
  });
}

/** POST /alerts/channels — X-MFA-Code required (doc 04 §2.11); secrets sealed, never echoed. */
export function useCreateChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { name: string; kind: ChannelKind; config: Record<string, string>; mfaCode: string }) =>
      post<AlertChannel>(
        "/alerts/channels",
        { name: args.name, kind: args.kind, config: args.config },
        { headers: { "X-MFA-Code": args.mfaCode } },
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ak.channels }),
  });
}

export function useDeleteChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => del<void>(`/alerts/channels/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ak.channels }),
  });
}

/** POST /alerts/channels/{id}/test — real notifier path; egress_blocked surfaces here. */
export function useTestChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => post<ChannelTestResult>(`/alerts/channels/${id}/test`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ak.channels }),
  });
}

export interface EventFilters {
  state?: string;
  rule_id?: string;
  severity?: string;
}

export function useEvents(filters: EventFilters) {
  const query = listQuery({ ...filters });
  return useQuery({
    queryKey: [...qk.alerts.events, query] as const,
    queryFn: () => get<EventsResponse>(`/alerts/events${query}`),
    staleTime: staleTimes.telemetry,
  });
}

export function useAckEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => post<void>(`/alerts/events/${id}/ack`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.alerts.root }),
  });
}
