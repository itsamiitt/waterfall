// Totality test (doc 08 §2): EVERY closed enum value maps to a {token, icon, label}
// descriptor — no enum member may reach the UI without icon + label (WCAG 1.4.1).
import { describe, expect, it } from "vitest";
import {
  ALERT_STATES,
  APPROVAL_STATUSES,
  CONFIG_VERSION_STATUSES,
  ERROR_CLASSES,
  INCLUSION_STATUSES,
  KEY_STATUSES,
  OP_STATES,
  statusMaps,
  unknownStatus,
  WORKER_STATUSES,
} from "./status";

const TOKENS = new Set(["ok", "warn", "error", "info", "neutral", "paused"]);

const CASES: [keyof typeof statusMaps, readonly string[]][] = [
  ["keyStatus", KEY_STATUSES],
  ["opState", OP_STATES],
  ["inclusionStatus", INCLUSION_STATUSES],
  ["workerStatus", WORKER_STATUSES],
  ["alertState", ALERT_STATES],
  ["approvalStatus", APPROVAL_STATUSES],
  ["configVersionStatus", CONFIG_VERSION_STATUSES],
  ["errorClass", ERROR_CLASSES],
];

describe("status map totality", () => {
  for (const [mapName, values] of CASES) {
    it(`${mapName} covers all ${values.length} values with token+icon+label`, () => {
      const map = statusMaps[mapName] as Record<string, { token: string; icon: string; label: string }>;
      // exact totality: no missing and no extra members
      expect(Object.keys(map).sort()).toEqual([...values].sort());
      for (const v of values) {
        const d = map[v]!;
        expect(TOKENS.has(d.token), `${mapName}.${v} token`).toBe(true);
        expect(d.icon.length, `${mapName}.${v} icon`).toBeGreaterThan(0);
        expect(d.label.length, `${mapName}.${v} label`).toBeGreaterThan(0);
      }
    });
  }

  it("expected enum sizes match the server CHECK constraints", () => {
    expect(KEY_STATUSES).toHaveLength(9); // migration 0005
    expect(OP_STATES).toHaveLength(4); // migration 0005
    expect(INCLUSION_STATUSES).toHaveLength(3); // ADR-0009
    expect(WORKER_STATUSES).toHaveLength(6); // migration 0008
    expect(ALERT_STATES).toHaveLength(2); // migration 0007
    expect(APPROVAL_STATUSES).toHaveLength(7); // migration 0007
    expect(ERROR_CLASSES).toHaveLength(8); // internal/domain 8-class taxonomy
  });

  it("unknownStatus degrades additive server values to a labelled neutral badge", () => {
    const d = unknownStatus("brand_new_state");
    expect(d.token).toBe("neutral");
    expect(d.label).toBe("brand_new_state");
  });
});
