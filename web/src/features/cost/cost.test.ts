// Cost unit tests (P11 gate): the export carries the on-screen filters verbatim (WYSIWYG),
// and the forecast is always labeled modeled/indicative — never presented as fact.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { buildCostQuery, costExportPath, costSummaryPath } from "./query";
import { ForecastTile } from "./forecast";
import type { CostFilters } from "./types";

const filters: CostFilters = {
  group_by: "provider",
  from: "2026-06-01T00:00:00Z",
  to: "2026-07-01T00:00:00Z",
  filter: { provider_id: "hunter" },
};

describe("cost export is WYSIWYG (doc 04 §2.10)", () => {
  it("export query is byte-identical to the summary query", () => {
    const q = buildCostQuery(filters);
    expect(costSummaryPath(filters)).toBe(`/cost/summary${q}`);
    expect(costExportPath(filters)).toBe(`/cost/export${q}`);
    // both derive from the same builder, so their querystrings are always equal
    expect(costExportPath(filters).replace("/cost/export", "")).toBe(
      costSummaryPath(filters).replace("/cost/summary", ""),
    );
  });

  it("carries group_by, range and active drill-down filters", () => {
    const q = buildCostQuery(filters);
    expect(q).toContain("group_by=provider");
    expect(q).toContain("from=2026-06-01T00%3A00%3A00Z");
    expect(q).toContain("to=2026-07-01T00%3A00%3A00Z");
    expect(q).toContain(`${encodeURIComponent("filter[provider_id]")}=hunter`);
  });

  it("changing the group_by / filter changes the exported query (no stale export)", () => {
    const other = buildCostQuery({ ...filters, group_by: "workflow", filter: {} });
    expect(other).not.toBe(buildCostQuery(filters));
    expect(other).toContain("group_by=workflow");
  });
});

describe("forecast is labeled modeled / indicative (never fact)", () => {
  it("linear projection carries modeled + indicative + UNVERIFIED labels", () => {
    const html = renderToStaticMarkup(
      createElement(ForecastTile, {
        forecast: { method: "linear", source: "modeled", eom_credits: 187400, band_low: 150000, band_high: 220000 },
      }),
    );
    expect(html).toContain("modeled");
    expect(html).toContain("indicative");
    expect(html).toContain("UNVERIFIED");
  });

  it("insufficient history renders a collecting-history state, not a forecast line", () => {
    const html = renderToStaticMarkup(
      createElement(ForecastTile, {
        forecast: { method: "insufficient_history", source: "modeled", days_of_history: 6 },
      }),
    );
    expect(html).toContain("Collecting history (6/14 days)");
    expect(html).not.toContain("UNVERIFIED");
  });
});
