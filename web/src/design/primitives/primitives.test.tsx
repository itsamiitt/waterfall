// Primitive render smokes via react-dom/server (no jsdom — not in the ADR-0016 allowlist).
// Static markup assertions cover the accessibility contract: labels always rendered, errors
// wired via aria-describedby, status never color-only (icon + label), spinner on loading.
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Badge } from "./Badge";
import { Button } from "./Button";
import { EmptyState } from "./EmptyState";
import { Input } from "./Input";
import { StatTile } from "./StatTile";
import { keyStatusInfo } from "../../lib/status";

describe("primitive render smokes", () => {
  it("Button: loading disables and shows a spinner", () => {
    const html = renderToStaticMarkup(
      <Button variant="primary" loading>
        Save
      </Button>,
    );
    expect(html).toContain("disabled");
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain("p-btn-spinner");
    expect(html).toContain("Save");
    expect(html).toContain('data-variant="primary"');
  });

  it("Badge: renders icon + label + token from lib/status.ts (never color-only)", () => {
    const d = keyStatusInfo("rate_limited");
    const html = renderToStaticMarkup(<Badge status={d.token} label={d.label} icon={d.icon} />);
    expect(html).toContain("<svg"); // icon glyph
    expect(html).toContain("Rate limited"); // text label
    expect(html).toContain('data-token="warn"');
    expect(html).toContain('data-family="filled"');
  });

  it("Badge: outlined family for the inclusion trichotomy is distinct", () => {
    const html = renderToStaticMarkup(
      <Badge status="ok" label="Active-candidate" icon="flag" family="outlined" />,
    );
    expect(html).toContain('data-family="outlined"');
  });

  it("Input: label is wired to the control; error binds via aria-describedby + role=alert", () => {
    const html = renderToStaticMarkup(
      <Input label="Email" value="x" onChange={() => {}} error="required" id="email" />,
    );
    expect(html).toContain('for="email"');
    expect(html).toContain('id="email"');
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('aria-describedby="email-err"');
    expect(html).toContain('role="alert"');
    expect(html).toContain("Email");
  });

  it("EmptyState: error variant shows the envelope code and is announced", () => {
    const html = renderToStaticMarkup(
      <EmptyState
        variant="error"
        title="Could not load"
        errorCode="rate_limited"
        action={{ label: "Retry", onClick: () => {} }}
      />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain("rate_limited");
    expect(html).toContain("Retry");
    expect(html).toContain('data-variant="error"');
  });

  it("StatTile: renders label, value, unit, delta direction, and sparkline", () => {
    const html = renderToStaticMarkup(
      <StatTile label="Requests today" value="1.28M" delta={4.2} spark={[1, 3, 2, 5]} />,
    );
    expect(html).toContain("Requests today");
    expect(html).toContain("1.28M");
    expect(html).toContain('data-direction="up"');
    expect(html).toContain("+4.2% vs yesterday");
    expect(html).toContain("<svg"); // sparkline
  });
});
