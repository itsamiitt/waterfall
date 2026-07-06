// features/keys — status + health chips (doc 09 §3.1, P9 acceptance #4). Key STATUS uses the
// canonical lib/status map (all 9 KM-3 states, color+icon+label, never color-only). HEALTH is a
// smaller separate enum (ok/warn/err/unknown) with a local descriptor — it is a different axis
// from status and is never conflated with it.
import { Badge } from "../../design/primitives";
import {
  KEY_STATUSES,
  keyStatusInfo,
  unknownStatus,
  type KeyStatus,
  type StatusDescriptor,
} from "../../lib/status";
import type { KeyHealth } from "./types";

const isKeyStatus = (s: string): s is KeyStatus => (KEY_STATUSES as readonly string[]).includes(s);

export function keyStatusDescriptor(status: string): StatusDescriptor {
  return isKeyStatus(status) ? keyStatusInfo(status) : unknownStatus(status);
}

const HEALTH: Record<string, StatusDescriptor> = {
  ok: { token: "ok", icon: "check", label: "ok" },
  warn: { token: "warn", icon: "triangle", label: "warn" },
  err: { token: "error", icon: "x", label: "err" },
  error: { token: "error", icon: "x", label: "err" },
  unknown: { token: "neutral", icon: "question", label: "—" },
};

export function keyHealthDescriptor(health: KeyHealth): StatusDescriptor {
  return HEALTH[health] ?? unknownStatus(String(health));
}

export function KeyStatusBadge({ status }: { status: string }) {
  const d = keyStatusDescriptor(status);
  return <Badge status={d.token} label={d.label} icon={d.icon} />;
}

export function KeyHealthBadge({ health }: { health: KeyHealth }) {
  const d = keyHealthDescriptor(health);
  return <Badge status={d.token} label={d.label} icon={d.icon} />;
}
