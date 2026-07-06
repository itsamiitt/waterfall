// features/alerts/types.ts — local types transcribed from doc 04 §2.11 (alerts).
import type { AlertOp, ChannelKind, Severity } from "./vocab";

export interface AlertRule {
  id: string;
  name: string;
  metric: string;
  scope: Record<string, string>;
  op: AlertOp;
  threshold: number;
  window_s: number;
  cooldown_s: number;
  severity: Severity;
  channels: string[];
  enabled: boolean;
  muted_until: string | null;
  updated_at?: string;
}

/** POST /alerts/rules body (doc 04 §2.11). */
export interface AlertRuleInput {
  name: string;
  metric: string;
  scope: Record<string, string>;
  op: AlertOp;
  threshold: number;
  window_s: number;
  cooldown_s: number;
  severity: Severity;
  channels: string[];
  enabled: boolean;
}

export interface RulesResponse {
  items: AlertRule[];
}

export interface AlertChannel {
  id: string;
  name: string;
  kind: ChannelKind;
  status: string;
  /** last test-send result surfaced in the row (doc 09 §12.1) — config is never echoed. */
  last_test?: { ok: boolean; response_code?: number; at?: string; error_code?: string };
}

export interface ChannelsResponse {
  items: AlertChannel[];
}

/** POST /alerts/channels/{id}/test result (doc 04 §2.11): delivery status + response code. */
export interface ChannelTestResult {
  ok: boolean;
  response_code?: number;
  /** e.g. egress_blocked when the SSRF guard rejects the destination (doc 04 §2.11). */
  error_code?: string;
  message?: string;
}

/** POST /alerts/rules/{id}/test result: would-fire + current value, no notification sent. */
export interface RuleTestResult {
  would_fire: boolean;
  value: number;
}

export interface AlertEvent {
  id: string;
  rule_id: string;
  rule_name?: string;
  state: "firing" | "resolved";
  severity: Severity;
  value: number;
  fired_at: string;
  resolved_at: string | null;
  acked_by?: string | null;
}

export interface EventsResponse {
  items: AlertEvent[];
}
