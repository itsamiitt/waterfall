// Alerts unit tests (P11 gate): the rule editor is a CLOSED-VOCABULARY picker set — no query
// language, no free metric box (doc 04 §2.11, doc 10 §4). The metric vocabulary is exactly 17.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { RuleEditor, defaultRuleInput } from "./RuleEditor";
import { METRIC_BY_ID, METRIC_VOCAB, OPS } from "./vocab";

describe("alert metric vocabulary (doc 10 §4)", () => {
  it("is closed at exactly 17 metrics including cost.anomaly", () => {
    expect(METRIC_VOCAB).toHaveLength(17);
    expect(METRIC_BY_ID.has("cost.anomaly")).toBe(true);
    expect(METRIC_BY_ID.has("provider.error_rate")).toBe(true);
  });

  it("seeds op/threshold/window from a metric's defaults; point-in-time metrics disable window", () => {
    const er = defaultRuleInput("provider.error_rate");
    expect(er.op).toBe("gt");
    expect(er.threshold).toBe(0.05);
    expect(er.window_s).toBe(600);
    expect(METRIC_BY_ID.get("provider.error_rate")!.windowApplies).toBe(true);
    expect(METRIC_BY_ID.get("provider.credits_remaining")!.windowApplies).toBe(false);
  });
});

describe("rule editor is a closed-vocabulary picker (no query language)", () => {
  const html = renderToStaticMarkup(
    createElement(RuleEditor, {
      initial: defaultRuleInput(),
      channels: [],
      onSubmit: () => {},
    }),
  );

  it("renders the metric as a <select> exposing all 17 metrics — never a free text box", () => {
    expect(html).toContain("<select");
    for (const m of METRIC_VOCAB) {
      expect(html, `metric option ${m.metric}`).toContain(`value="${m.metric}"`);
    }
  });

  it("has NO free-form query input (no textarea, no query language)", () => {
    expect(html).not.toContain("<textarea");
    expect(html.toLowerCase()).toContain("no query language");
  });

  it("exposes op as a closed operator picker", () => {
    for (const o of OPS) expect(html).toContain(`value="${o.value}"`);
  });
});
