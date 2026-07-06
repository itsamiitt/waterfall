import { Icon, type IconName } from "./Icon";
import type { StatusToken } from "../../lib/status";

export interface BadgeProps {
  /** Consumes lib/status.ts output only — no free-form colors (doc 08 §6.2). */
  status: StatusToken;
  label: string;
  icon: IconName;
  /** `outlined` is reserved for the ADR-0009 inclusion trichotomy; runtime states are
   * `filled` — the two axes are never conflated (doc 08 §6.1). */
  family?: "filled" | "outlined";
}

export function Badge({ status, label, icon, family = "filled" }: BadgeProps) {
  return (
    <span className="p-badge" data-token={status} data-family={family}>
      <Icon name={icon} />
      {label}
    </span>
  );
}
