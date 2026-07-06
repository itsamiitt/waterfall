// Dual-badge tests (P9 acceptance #4): inclusion chip + op_state chip + SERVER-computed
// availability, rendered as three distinct chips and never conflated.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { availabilityDescriptor, inclusionDescriptor, opStateDescriptor, ProviderBadges } from "./badges";

describe("availabilityDescriptor (never derived client-side)", () => {
  it("available true → ok/check", () => {
    const d = availabilityDescriptor(true, null);
    expect(d.token).toBe("ok");
    expect(d.label).toBe("available");
    expect(d.title).toBeUndefined();
  });

  it("available false surfaces the failed conjunct verbatim as the title", () => {
    const d = availabilityDescriptor(false, "op_state_paused");
    expect(d.token).toBe("error");
    expect(d.label).toBe("unavailable");
    expect(d.title).toBe("op_state_paused");
  });

  it("available false without a reason still renders unavailable", () => {
    expect(availabilityDescriptor(false, null).title).toBe("unavailable");
  });
});

describe("inclusion vs op_state descriptors", () => {
  it("known inclusion values map through lib/status", () => {
    expect(inclusionDescriptor("ACTIVE-CANDIDATE").token).toBe("ok");
    expect(inclusionDescriptor("EXCLUDED").label).toBe("Excluded");
  });
  it("unknown server value degrades to a labelled neutral chip", () => {
    expect(inclusionDescriptor("FUTURE_STATE").token).toBe("neutral");
    expect(opStateDescriptor("weird").label).toBe("weird");
  });
});

describe("ProviderBadges renders three distinct chips", () => {
  const html = renderToStaticMarkup(
    <ProviderBadges status="ACTIVE-CANDIDATE" opState="paused" effectiveAvailable={false} unavailableReason="op_state_paused" />,
  );
  it("inclusion is OUTLINED, op_state + availability are FILLED (axes not conflated)", () => {
    expect(html).toContain('data-family="outlined"');
    expect(html).toContain('data-family="filled"');
  });
  it("shows availability label and the failed-conjunct title", () => {
    expect(html).toContain("unavailable");
    expect(html).toContain("op_state_paused");
  });
  it("each chip carries an icon + text label (never color-only)", () => {
    expect(html).toContain("<svg");
    expect(html).toContain("Active-candidate");
    expect(html).toContain("Paused");
  });
});
