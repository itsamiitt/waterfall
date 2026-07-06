// features/providers/badges.tsx — the DUAL badge (doc 09 §2.1, P9 acceptance #4). Three axes
// are rendered side by side and NEVER conflated (doc 08 §6.1):
//   1. status  — ADR-0009 inclusion trichotomy → OUTLINED chip (lib/status inclusionStatus)
//   2. op_state — runtime state              → FILLED chip   (lib/status opState)
//   3. effective_available — SERVER-computed availability, never derived client-side; rendered
//      from the boolean the API returns, with unavailable_reason naming the failed conjunct.
import { Badge } from "../../design/primitives";
import {
  inclusionStatusInfo,
  opStateInfo,
  unknownStatus,
  type InclusionStatus,
  type OpState,
  type StatusDescriptor,
} from "../../lib/status";
import { INCLUSION_STATUSES, OP_STATES } from "../../lib/status";

const isInclusion = (s: string): s is InclusionStatus =>
  (INCLUSION_STATUSES as readonly string[]).includes(s);
const isOpState = (s: string): s is OpState => (OP_STATES as readonly string[]).includes(s);

/** Inclusion trichotomy descriptor (outlined family); unknown server value → neutral label. */
export function inclusionDescriptor(status: string): StatusDescriptor {
  return isInclusion(status) ? inclusionStatusInfo(status) : unknownStatus(status);
}

/** Runtime op_state descriptor (filled family). */
export function opStateDescriptor(op: string): StatusDescriptor {
  return isOpState(op) ? opStateInfo(op) : unknownStatus(op);
}

/** Availability chip built PURELY from the server fields — the client never computes the
 * boolean. `unavailable_reason` (the failed conjunct) becomes the label/title when false. */
export function availabilityDescriptor(
  effectiveAvailable: boolean,
  unavailableReason: string | null | undefined,
): StatusDescriptor & { title?: string } {
  if (effectiveAvailable) {
    return { token: "ok", icon: "check", label: "available" };
  }
  const reason = unavailableReason ?? "unavailable";
  return { token: "error", icon: "x", label: "unavailable", title: reason };
}

export interface ProviderBadgesProps {
  status: string;
  opState?: string;
  effectiveAvailable: boolean;
  unavailableReason?: string | null;
}

export function ProviderBadges({
  status,
  opState,
  effectiveAvailable,
  unavailableReason,
}: ProviderBadgesProps) {
  const inc = inclusionDescriptor(status);
  const op = opState !== undefined ? opStateDescriptor(opState) : null;
  const avail = availabilityDescriptor(effectiveAvailable, unavailableReason);
  return (
    <span className="provider-badges">
      <Badge status={inc.token} label={inc.label} icon={inc.icon} family="outlined" />
      {op ? <Badge status={op.token} label={op.label} icon={op.icon} family="filled" /> : null}
      <span title={avail.title} className="provider-avail">
        <Badge status={avail.token} label={avail.label} icon={avail.icon} family="filled" />
      </span>
    </span>
  );
}
