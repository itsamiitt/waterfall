// lib/status.ts — THE single mapping of every closed enum to {token, icon, label}
// (doc 08 §2, OI-UI-2; doc 08 §6.1: status is never color-only — Badge/StatTile always render
// icon + label with the color). Enum values mirror the server CHECK constraints
// (migrations 0005–0008) and internal/domain ErrorClass. Totality is unit-tested.

export type StatusToken = "ok" | "warn" | "error" | "info" | "neutral" | "paused";

export type IconName =
  | "check"
  | "x"
  | "slash"
  | "pause"
  | "clock"
  | "wrench"
  | "flag"
  | "triangle"
  | "question"
  | "gauge"
  | "shield"
  | "refresh"
  | "archive"
  | "dot";

export interface StatusDescriptor {
  token: StatusToken;
  icon: IconName;
  label: string;
}

// --- Provider Key status (migration 0005 CHECK; KM-3 lifecycle) ---
export const KEY_STATUSES = [
  "active",
  "disabled",
  "paused",
  "exhausted",
  "rate_limited",
  "auth_failed",
  "expired",
  "rotating",
  "archived",
] as const;
export type KeyStatus = (typeof KEY_STATUSES)[number];

const keyStatus: Record<KeyStatus, StatusDescriptor> = {
  active: { token: "ok", icon: "check", label: "Active" },
  disabled: { token: "neutral", icon: "slash", label: "Disabled" },
  paused: { token: "paused", icon: "pause", label: "Paused" },
  exhausted: { token: "warn", icon: "gauge", label: "Exhausted" },
  rate_limited: { token: "warn", icon: "clock", label: "Rate limited" },
  auth_failed: { token: "error", icon: "shield", label: "Auth failed" },
  expired: { token: "error", icon: "clock", label: "Expired" },
  rotating: { token: "info", icon: "refresh", label: "Rotating" },
  archived: { token: "neutral", icon: "archive", label: "Archived" },
};

// --- Provider runtime op_state (migration 0005 CHECK; filled badge family) ---
export const OP_STATES = ["enabled", "disabled", "paused", "maintenance"] as const;
export type OpState = (typeof OP_STATES)[number];

const opState: Record<OpState, StatusDescriptor> = {
  enabled: { token: "ok", icon: "check", label: "Enabled" },
  disabled: { token: "neutral", icon: "slash", label: "Disabled" },
  paused: { token: "paused", icon: "pause", label: "Paused" },
  maintenance: { token: "info", icon: "wrench", label: "Maintenance" },
};

// --- ADR-0009 inclusion trichotomy (outlined badge family — never conflated with op_state) ---
export const INCLUSION_STATUSES = ["ACTIVE-CANDIDATE", "DEPRIORITIZED", "EXCLUDED"] as const;
export type InclusionStatus = (typeof INCLUSION_STATUSES)[number];

const inclusionStatus: Record<InclusionStatus, StatusDescriptor> = {
  "ACTIVE-CANDIDATE": { token: "ok", icon: "flag", label: "Active-candidate" },
  DEPRIORITIZED: { token: "warn", icon: "triangle", label: "Deprioritized" },
  EXCLUDED: { token: "neutral", icon: "slash", label: "Excluded" },
};

// --- Worker status + desired_state (migration 0008 CHECK) ---
export const WORKER_STATUSES = [
  "starting",
  "running",
  "draining",
  "paused",
  "stopped",
  "lost",
] as const;
export type WorkerStatus = (typeof WORKER_STATUSES)[number];

const workerStatus: Record<WorkerStatus, StatusDescriptor> = {
  starting: { token: "info", icon: "refresh", label: "Starting" },
  running: { token: "ok", icon: "check", label: "Running" },
  draining: { token: "info", icon: "clock", label: "Draining" },
  paused: { token: "paused", icon: "pause", label: "Paused" },
  stopped: { token: "neutral", icon: "slash", label: "Stopped" },
  lost: { token: "error", icon: "x", label: "Lost" },
};

// --- Alert episode state (migration 0007 CHECK) ---
export const ALERT_STATES = ["firing", "resolved"] as const;
export type AlertState = (typeof ALERT_STATES)[number];

const alertState: Record<AlertState, StatusDescriptor> = {
  firing: { token: "error", icon: "triangle", label: "Firing" },
  resolved: { token: "ok", icon: "check", label: "Resolved" },
};

// --- Approval request status (migration 0007 CHECK) ---
export const APPROVAL_STATUSES = [
  "pending",
  "approved",
  "rejected",
  "expired",
  "cancelled",
  "executed",
  "failed",
] as const;
export type ApprovalStatus = (typeof APPROVAL_STATUSES)[number];

const approvalStatus: Record<ApprovalStatus, StatusDescriptor> = {
  pending: { token: "warn", icon: "clock", label: "Pending" },
  approved: { token: "info", icon: "check", label: "Approved" },
  rejected: { token: "error", icon: "x", label: "Rejected" },
  expired: { token: "neutral", icon: "clock", label: "Expired" },
  cancelled: { token: "neutral", icon: "slash", label: "Cancelled" },
  executed: { token: "ok", icon: "check", label: "Executed" },
  failed: { token: "error", icon: "triangle", label: "Failed" },
};

// --- Config version lifecycle (migration 0006 CHECK; doc 03 §9.3) ---
export const CONFIG_VERSION_STATUSES = ["draft", "validated", "published", "archived"] as const;
export type ConfigVersionStatus = (typeof CONFIG_VERSION_STATUSES)[number];

const configVersionStatus: Record<ConfigVersionStatus, StatusDescriptor> = {
  draft: { token: "neutral", icon: "dot", label: "Draft" },
  validated: { token: "info", icon: "check", label: "Validated" },
  published: { token: "ok", icon: "flag", label: "Published" },
  archived: { token: "neutral", icon: "archive", label: "Archived" },
};

// --- Provider error taxonomy (internal/domain ErrorClass String() values) ---
export const ERROR_CLASSES = [
  "UNKNOWN",
  "AUTH",
  "RATE_LIMIT",
  "TRANSIENT",
  "NOT_FOUND",
  "BAD_REQUEST",
  "QUOTA",
  "PROVIDER_DOWN",
] as const;
export type ErrorClass = (typeof ERROR_CLASSES)[number];

const errorClass: Record<ErrorClass, StatusDescriptor> = {
  UNKNOWN: { token: "neutral", icon: "question", label: "Unknown" },
  AUTH: { token: "error", icon: "shield", label: "Auth" },
  RATE_LIMIT: { token: "warn", icon: "clock", label: "Rate limit" },
  TRANSIENT: { token: "warn", icon: "refresh", label: "Transient" },
  NOT_FOUND: { token: "neutral", icon: "slash", label: "Not found" },
  BAD_REQUEST: { token: "error", icon: "x", label: "Bad request" },
  QUOTA: { token: "warn", icon: "gauge", label: "Quota" },
  PROVIDER_DOWN: { token: "error", icon: "triangle", label: "Provider down" },
};

export const statusMaps = {
  keyStatus,
  opState,
  inclusionStatus,
  workerStatus,
  alertState,
  approvalStatus,
  configVersionStatus,
  errorClass,
} as const;

export const keyStatusInfo = (s: KeyStatus): StatusDescriptor => keyStatus[s];
export const opStateInfo = (s: OpState): StatusDescriptor => opState[s];
export const inclusionStatusInfo = (s: InclusionStatus): StatusDescriptor => inclusionStatus[s];
export const workerStatusInfo = (s: WorkerStatus): StatusDescriptor => workerStatus[s];
export const alertStateInfo = (s: AlertState): StatusDescriptor => alertState[s];
export const approvalStatusInfo = (s: ApprovalStatus): StatusDescriptor => approvalStatus[s];
export const configVersionStatusInfo = (s: ConfigVersionStatus): StatusDescriptor =>
  configVersionStatus[s];
export const errorClassInfo = (s: ErrorClass): StatusDescriptor => errorClass[s];

/** Fallback for values arriving from the server that the UI vocabulary does not know yet
 * (additive server change): renders as a neutral, labelled badge rather than crashing. */
export const unknownStatus = (raw: string): StatusDescriptor => ({
  token: "neutral",
  icon: "question",
  label: raw,
});
