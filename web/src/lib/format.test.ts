import { describe, expect, it } from "vitest";
import {
  formatCompact,
  formatCount,
  formatDeltaPct,
  formatDurationS,
  formatLatencyMs,
  formatPercent,
  formatUtc,
  formatUtcTime,
  relativeTime,
} from "./format";

describe("format helpers (UTC everywhere, doc 04 §1.1)", () => {
  it("formats UTC timestamps without local-time drift", () => {
    expect(formatUtc("2026-07-02T12:40:02Z")).toBe("2026-07-02 12:40:02Z");
    expect(formatUtcTime("2026-07-02T12:40:02Z")).toBe("12:40:02Z");
    expect(formatUtc("not a date")).toBe("—");
  });

  it("renders relative age like the wireframes (41s ago)", () => {
    const now = new Date("2026-07-02T12:40:43Z");
    expect(relativeTime("2026-07-02T12:40:02Z", now)).toBe("41s ago");
    expect(relativeTime("2026-07-02T12:35:02Z", now)).toBe("5m ago");
    expect(relativeTime("2026-07-02T10:40:02Z", now)).toBe("2h ago");
    expect(relativeTime("2026-06-29T12:40:02Z", now)).toBe("3d ago");
    expect(relativeTime("2027-01-01T00:00:00Z", now)).toBe("—"); // future
  });

  it("formats counts, compact values, and credits", () => {
    expect(formatCount(88410)).toBe("88,410");
    expect(formatCount(undefined)).toBe("—");
    expect(formatCompact(1284031)).toBe("1.28M");
    expect(formatCompact(14900)).toBe("14.9K");
    expect(formatCompact(341)).toBe("341");
  });

  it("formats percentages from fractions and percent deltas", () => {
    expect(formatPercent(0.943)).toBe("94.3%");
    expect(formatPercent(81, { fromPercent: true, digits: 0 })).toBe("81%");
    expect(formatDeltaPct(4.2)).toBe("+4.2%");
    expect(formatDeltaPct(-1.5)).toBe("-1.5%");
  });

  it("formats latency and durations", () => {
    expect(formatLatencyMs(412)).toBe("412ms");
    expect(formatLatencyMs(1840)).toBe("1.84s");
    expect(formatDurationS(41)).toBe("41s");
    expect(formatDurationS(341)).toBe("5m 41s");
    expect(formatDurationS(7260)).toBe("2h 1m");
  });
});
