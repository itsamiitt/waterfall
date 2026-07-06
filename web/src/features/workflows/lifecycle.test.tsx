// Workflow lifecycle + dry-run render tests (doc 12 §P10 gate). Pure logic + a render smoke via
// react-dom/server (vitest env is node; no jsdom).
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  canvasNodes,
  dryRunZeroEgress,
  publishGate,
  reorderSequential,
  reportSeverity,
} from "./lifecycle";
import { WorkflowDryRun } from "./WorkflowDryRun";
import type { DryRunResult, WaterfallWorkflowPayload } from "./types";

const PAYLOAD: WaterfallWorkflowPayload = {
  schema_version: 1,
  name: "work_email-default",
  trigger: "api",
  fields: ["work_email", "mobile_phone"],
  entry_provider: "hunter",
  parallel_providers: ["prospeo", "dropcontact"],
  sequential_providers: ["twilio-lu", "clearbit-x"],
  fallback_provider: "snov",
  timeout_ms: 8000,
  confidence_threshold: 0.85,
  max_cost_credits: 5,
  max_providers: 4,
  stop_conditions: ["target-met", "ceiling"],
};

const DRY_RUN: DryRunResult = {
  zero_egress: true,
  resolved_scope: {
    levels_consulted: ["tenant+country", "tenant", "default"],
    overrides: { hunter: { effective: "off", source: "tenant", source_version: 7 } },
  },
  by_field: {
    work_email: [
      { provider: "hunter", cost_credits: 1, expected_confidence: 0.9 },
      { provider: "prospeo", cost_credits: 1, expected_confidence: 0.86 },
    ],
    mobile_phone: [{ provider: "twilio-lu", cost_credits: 2, expected_confidence: 0.88 }],
  },
  max_committed_credits: 4,
  stop_projection: { condition: "target-met", expected_providers_used: 2 },
  warnings: [],
  diff_vs_active: { provider_order_changed: true, removed: [], added: ["prospeo"] },
};

describe("publishGate — gated on server validate (P10 AC #4)", () => {
  it("draft cannot publish; validated+clean can; edited-since-validate cannot", () => {
    expect(publishGate("draft", false, 0).canPublish).toBe(false);
    expect(publishGate("validated", false, 0).canPublish).toBe(true);
    expect(publishGate("validated", true, 0).canPublish).toBe(false);
    expect(publishGate("published", false, 0).canPublish).toBe(false);
  });
});

describe("reportSeverity", () => {
  it("errors dominate; warnings next; else ok", () => {
    expect(reportSeverity({ validated_at: "", payload_hash: "", errors: [{ rule: "VR-5", code: "c", severity: "error", path: "/x", message: "m" }], warnings: [] })).toBe("error");
    expect(reportSeverity(null)).toBe("ok");
  });
});

describe("canvas model", () => {
  it("derives stepped nodes entry→parallel→sequential→fallback", () => {
    const nodes = canvasNodes(PAYLOAD);
    expect(nodes.map((n) => n.step)).toEqual(["entry", "parallel", "parallel", "sequential", "sequential", "fallback"]);
    expect(nodes[0]).toMatchObject({ step: "entry", provider: "hunter" });
  });

  it("reorderSequential swaps only within the sequential step", () => {
    const next = reorderSequential(PAYLOAD, "clearbit-x", "twilio-lu");
    expect(next.sequential_providers).toEqual(["clearbit-x", "twilio-lu"]);
    expect(next.entry_provider).toBe("hunter"); // other steps untouched
  });
});

describe("dry-run render (P10 AC #3) — order + cost/Confidence + provenance + zero egress", () => {
  it("guard: zero egress asserted", () => {
    expect(dryRunZeroEgress(DRY_RUN)).toBe(true);
    expect(dryRunZeroEgress({ ...DRY_RUN, zero_egress: false })).toBe(false);
  });

  it("renders provider order with cost/Confidence per Field", () => {
    const html = renderToStaticMarkup(<WorkflowDryRun result={DRY_RUN} />);
    expect(html).toContain("work_email");
    expect(html).toContain("hunter");
    expect(html).toContain("1cr @ 0.90");
    expect(html).toContain("prospeo");
    expect(html).toContain("mobile_phone");
    expect(html).toContain("twilio-lu");
    expect(html).toContain("2cr @ 0.88");
  });

  it("surfaces zero egress (backend guarantee) and expected total cost", () => {
    const html = renderToStaticMarkup(<WorkflowDryRun result={DRY_RUN} />);
    expect(html).toContain("zero egress");
    expect(html).toContain("4"); // expected_total_cost_credits
    expect(html).toContain("target-met");
  });

  it("renders provenance of inherited values from the resolver (not client-derived)", () => {
    const html = renderToStaticMarkup(<WorkflowDryRun result={DRY_RUN} />);
    expect(html).toContain("Provenance of inherited values");
    expect(html).toContain("tenant+country");
    expect(html).toContain("hunter");
    expect(html).toContain("off — from tenant, v7");
  });

  it("empty state prompts a dry-run with no Provider calls", () => {
    const html = renderToStaticMarkup(<WorkflowDryRun result={null} />);
    expect(html).toContain("no Provider calls are made");
  });
});
