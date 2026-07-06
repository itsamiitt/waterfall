// Timeline model tests (doc 09 §5.3): no_data is a DISTINCT visual from up.
import { describe, expect, it } from "vitest";
import { overallUptimePct, padDays, segmentStyle, segmentTitle, type DaySegment } from "./timelineModel";

describe("segmentStyle — no_data is never up", () => {
  it("up is ok and NOT no_data", () => {
    const s = segmentStyle("up");
    expect(s.token).toBe("ok");
    expect(s.noData).toBe(false);
  });
  it("no_data is flagged and tokened distinctly from up", () => {
    const nd = segmentStyle("no_data");
    expect(nd.noData).toBe(true);
    expect(nd.token).not.toBe(segmentStyle("up").token);
  });
  it("degraded/down map to warn/error", () => {
    expect(segmentStyle("degraded").token).toBe("warn");
    expect(segmentStyle("down").token).toBe("error");
  });
  it("unknown state degrades to the no_data treatment, not up", () => {
    const u = segmentStyle("weird");
    expect(u.noData).toBe(true);
  });
});

describe("padDays", () => {
  it("pads the front with no_data up to count", () => {
    const days: DaySegment[] = [{ date: "2026-07-01", status: "up", uptime_pct: 100 }];
    const padded = padDays(days, 90);
    expect(padded).toHaveLength(90);
    expect(padded[0]!.status).toBe("no_data");
    expect(padded[89]!.status).toBe("up");
  });
  it("slices when there are more than count", () => {
    const days: DaySegment[] = Array.from({ length: 100 }, (_, i) => ({ date: `d${i}`, status: "up" }));
    expect(padDays(days, 90)).toHaveLength(90);
  });
});

describe("overallUptimePct", () => {
  it("excludes no_data segments from the average", () => {
    const days: DaySegment[] = [
      { date: "a", status: "up", uptime_pct: 100 },
      { date: "b", status: "down", uptime_pct: 50 },
      { date: "c", status: "no_data" },
    ];
    expect(overallUptimePct(days)).toBe(75);
  });
  it("returns null when there is no data at all", () => {
    expect(overallUptimePct([{ date: "", status: "no_data" }])).toBeNull();
  });
});

describe("segmentTitle", () => {
  it("summarizes status, uptime, worst error and check count", () => {
    const t = segmentTitle({ date: "2026-07-01", status: "degraded", uptime_pct: 98, worst_error_class: "RATE_LIMIT", check_count: 24 });
    expect(t).toContain("degraded");
    expect(t).toContain("98% uptime");
    expect(t).toContain("RATE_LIMIT");
    expect(t).toContain("24 checks");
  });
});
