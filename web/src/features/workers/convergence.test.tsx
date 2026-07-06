// Worker desired-vs-actual convergence tests (doc 12 §P10 gate). Pure logic + a render smoke of
// the badge via react-dom/server (vitest env is node; no jsdom).
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Badge } from "../../design/primitives";
import { CONVERGENCE_WARN_S, desiredStatus, isHeartbeatStale, workerConvergence } from "./convergence";
import type { Worker } from "./types";

const base: Pick<Worker, "status" | "desired_state" | "converging_for_s" | "converging"> = {
  status: "running",
  desired_state: "running",
};

describe("workerConvergence — status vs desired_state (doc 09 §9)", () => {
  it("matched status/desired: not converging, plain status token", () => {
    const c = workerConvergence({ ...base, status: "running", desired_state: "running" });
    expect(c.converging).toBe(false);
    expect(c.token).toBe("ok");
    expect(c.label).toBe("Running");
  });

  it("status trails desired (paused desired, still running): converging (info)", () => {
    const c = workerConvergence({ status: "running", desired_state: "paused" });
    expect(c.converging).toBe(true);
    expect(c.escalated).toBe(false);
    expect(c.token).toBe("info");
    expect(c.label).toBe("Converging");
  });

  it("draining desired matches draining status: converged", () => {
    expect(workerConvergence({ status: "draining", desired_state: "draining" }).converging).toBe(false);
  });

  it("lost short-circuits to the error token regardless of desired_state", () => {
    const c = workerConvergence({ status: "lost", desired_state: "running" });
    expect(c.converging).toBe(false);
    expect(c.escalated).toBe(true);
    expect(c.token).toBe("error");
    expect(c.label).toBe("Lost");
  });

  it("converging past 5 minutes escalates to warn (not error) with runbook intent", () => {
    const c = workerConvergence({ status: "running", desired_state: "paused", converging_for_s: CONVERGENCE_WARN_S + 1 });
    expect(c.converging).toBe(true);
    expect(c.escalated).toBe(true);
    expect(c.token).toBe("warn");
    expect(c.label).toMatch(/stalled/i);
  });

  it("server converging flag forces the converging badge even if status equals target", () => {
    const c = workerConvergence({ status: "running", desired_state: "running", converging: true });
    expect(c.converging).toBe(true);
  });
});

describe("desiredStatus mapping", () => {
  it("maps each desired_state to its target status", () => {
    expect(desiredStatus("running")).toBe("running");
    expect(desiredStatus("paused")).toBe("paused");
    expect(desiredStatus("stopped")).toBe("stopped");
    expect(desiredStatus("draining")).toBe("draining");
  });
});

describe("isHeartbeatStale", () => {
  it("flags heartbeat age past the lost threshold", () => {
    expect(isHeartbeatStale({ heartbeat_age_s: 97 })).toBe(true);
    expect(isHeartbeatStale({ heartbeat_age_s: 9 })).toBe(false);
  });
});

describe("convergence badge render", () => {
  it("renders the converging badge with icon + label (never color-only)", () => {
    const c = workerConvergence({ status: "running", desired_state: "paused" });
    const html = renderToStaticMarkup(<Badge status={c.token} label={c.label} icon={c.icon} />);
    expect(html).toContain("Converging");
    expect(html).toContain("<svg"); // icon
    expect(html).toContain('data-token="info"');
  });
});
