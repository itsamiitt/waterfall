// Status rendering tests (P9 acceptance #4): all 9 key statuses render color+icon+label from
// lib/status.ts; health is a distinct axis.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { KEY_STATUSES } from "../../lib/status";
import { KeyHealthBadge, KeyStatusBadge, keyHealthDescriptor, keyStatusDescriptor } from "./statusCells";

describe("key status rendering (all 9 KM-3 states)", () => {
  it("covers every status with token + icon + label, never color-only", () => {
    expect(KEY_STATUSES).toHaveLength(9);
    for (const s of KEY_STATUSES) {
      const d = keyStatusDescriptor(s);
      expect(d.label.length).toBeGreaterThan(0);
      const html = renderToStaticMarkup(<KeyStatusBadge status={s} />);
      expect(html).toContain("<svg"); // icon glyph
      expect(html).toContain(`data-token="${d.token}"`);
      expect(html).toContain(d.label); // text label present
    }
  });
  it("unknown status degrades to a neutral labelled chip", () => {
    expect(keyStatusDescriptor("brand_new").token).toBe("neutral");
  });
});

describe("key health rendering (distinct axis)", () => {
  it("maps ok/warn/err/unknown to tokens", () => {
    expect(keyHealthDescriptor("ok").token).toBe("ok");
    expect(keyHealthDescriptor("warn").token).toBe("warn");
    expect(keyHealthDescriptor("err").token).toBe("error");
    expect(keyHealthDescriptor("unknown").token).toBe("neutral");
  });
  it("renders icon + label", () => {
    const html = renderToStaticMarkup(<KeyHealthBadge health="warn" />);
    expect(html).toContain("<svg");
    expect(html).toContain('data-token="warn"');
  });
});
