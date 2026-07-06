// Routing lifecycle unit tests (doc 12 §P10 gate). Pure logic + one render smoke via
// react-dom/server (vitest env is node; no jsdom — mirrors the P8 test style).
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { describeEffective, effectiveToken, moveItem, publishGate, reportSeverity } from "./lifecycle";
import { TriStateControl } from "./TriState";
import type { EffectiveOverride, ValidationReport } from "./types";

describe("publishGate — Publish is gated on server validation (P10 AC #4)", () => {
  it("draft: publish disabled, validate available", () => {
    const g = publishGate("draft", false, 0);
    expect(g.canPublish).toBe(false);
    expect(g.canValidate).toBe(true);
    expect(g.reason).toMatch(/validate/i);
  });

  it("validated + clean: publish enabled with no reason", () => {
    const g = publishGate("validated", false, 0);
    expect(g.canPublish).toBe(true);
    expect(g.reason).toBeNull();
  });

  it("validated but edited since validate: publish disabled (reverts to draft)", () => {
    const g = publishGate("validated", true, 0);
    expect(g.canPublish).toBe(false);
    expect(g.reason).toMatch(/re-validate/i);
  });

  it("validated with residual errors: publish disabled (defensive)", () => {
    const g = publishGate("validated", false, 2);
    expect(g.canPublish).toBe(false);
  });

  it("published/archived: nothing publishable", () => {
    expect(publishGate("published", false, 0).canPublish).toBe(false);
    expect(publishGate("archived", false, 0).canValidate).toBe(false);
  });
});

describe("reportSeverity", () => {
  const base: Omit<ValidationReport, "errors" | "warnings"> = {
    validated_at: "2026-07-02T11:03:00Z",
    payload_hash: "abc",
  };
  it("errors dominate warnings", () => {
    expect(reportSeverity({ ...base, errors: [{ rule: "VR-2", code: "x", severity: "error", path: "/a", message: "m" }], warnings: [] })).toBe("error");
  });
  it("warnings only", () => {
    expect(reportSeverity({ ...base, errors: [], warnings: [{ rule: "VR-12", code: "x", severity: "warning", path: "/a", message: "m" }] })).toBe("warn");
  });
  it("clean", () => {
    expect(reportSeverity({ ...base, errors: [], warnings: [] })).toBe("ok");
    expect(reportSeverity(null)).toBe("ok");
  });
});

describe("tri-state resolver display — effective value + source scope (never client-derived)", () => {
  it("renders 'off — inherited from tenant default, v7'", () => {
    const o: EffectiveOverride = { effective: "off", source: "tenant default", source_version: 7 };
    expect(describeEffective(o)).toBe("off — inherited from tenant default, v7");
    expect(effectiveToken(o)).toBe("neutral");
  });

  it("engine_default uses 'from engine default' phrasing", () => {
    const o: EffectiveOverride = { effective: "on", source: "engine_default" };
    expect(describeEffective(o)).toBe("on — from engine default");
    expect(effectiveToken(o)).toBe("ok");
  });

  it("TriStateControl renders the resolved effective value + source from the resolver", () => {
    const html = renderToStaticMarkup(
      <TriStateControl
        provider="hunter"
        mode="inherit"
        onChange={() => {}}
        effective={{ effective: "off", source: "tenant default", source_version: 7 }}
      />,
    );
    // resolved effective value + provenance shown verbatim
    expect(html).toContain("off — inherited from tenant default, v7");
    // tri-state radios present and keyboard-operable
    expect(html).toContain('role="radiogroup"');
    expect(html).toContain('role="radio"');
    // inherit is the current selection
    expect(html).toContain('aria-checked="true"');
  });

  it("absent resolver output shows an honest placeholder, not a derived value", () => {
    const html = renderToStaticMarkup(
      <TriStateControl provider="prospeo" mode="on" onChange={() => {}} />,
    );
    expect(html).toContain("no resolved value yet");
  });
});

describe("moveItem — pure dnd reorder", () => {
  it("moves active to over's slot", () => {
    expect(moveItem(["a", "b", "c", "d"], "a", "c")).toEqual(["b", "c", "a", "d"]);
  });
  it("no-op when ids missing or equal", () => {
    expect(moveItem(["a", "b"], "a", "a")).toEqual(["a", "b"]);
    expect(moveItem(["a", "b"], "z", "a")).toEqual(["a", "b"]);
  });
});
