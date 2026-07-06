// Token contrast gate (doc 08 §6.1, OI-UI-3 — a named P8 gate): status/text foreground
// tokens must meet WCAG AA against their paired surfaces in BOTH themes — >= 4.5:1 for text,
// >= 3:1 for large text and UI glyphs. Parses tokens.css directly so the test can never
// drift from the shipped values.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "tokens.css"), "utf8");

function block(selector: string): Record<string, string> {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`selector ${selector} not found`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("}", open);
  const body = css.slice(open + 1, close);
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g)) {
    out[m[1]!] = m[2]!.trim();
  }
  return out;
}

function srgbChannel(v: number): number {
  const c = v / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const h = hex.replace("#", "");
  const full = h.length === 3 ? h.split("").map((c) => c + c).join("") : h;
  const r = parseInt(full.slice(0, 2), 16);
  const g = parseInt(full.slice(2, 4), 16);
  const b = parseInt(full.slice(4, 6), 16);
  return 0.2126 * srgbChannel(r) + 0.7152 * srgbChannel(g) + 0.0722 * srgbChannel(b);
}

function contrast(fg: string, bg: string): number {
  const l1 = luminance(fg);
  const l2 = luminance(bg);
  const [hi, lo] = l1 >= l2 ? [l1, l2] : [l2, l1];
  return (hi + 0.05) / (lo + 0.05);
}

const STATUSES = ["ok", "warn", "error", "info", "neutral", "paused"] as const;
const THEMES = [
  ["light", block(":root")],
  ["dark", block('[data-theme="dark"]')],
] as const;

describe.each(THEMES)("token contrast — %s theme", (_name, t) => {
  it("body text meets 4.5:1 on every surface", () => {
    for (const surface of ["color-bg", "color-bg-raised", "color-bg-sunken"]) {
      expect(
        contrast(t["color-text"]!, t[surface]!),
        `color-text on ${surface}`,
      ).toBeGreaterThanOrEqual(4.5);
      expect(
        contrast(t["color-text-muted"]!, t[surface]!),
        `color-text-muted on ${surface}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("faint text meets 3:1 (large text / secondary glyphs only)", () => {
    expect(contrast(t["color-text-faint"]!, t["color-bg"]!)).toBeGreaterThanOrEqual(3);
  });

  it("accent button text meets 4.5:1 on the accent surface", () => {
    expect(contrast(t["color-accent-text"]!, t["color-accent"]!)).toBeGreaterThanOrEqual(4.5);
  });

  it.each([...STATUSES])("status-%s text meets 4.5:1 on its badge surface", (s) => {
    expect(contrast(t[`status-${s}`]!, t[`status-${s}-bg`]!)).toBeGreaterThanOrEqual(4.5);
  });

  it.each([...STATUSES])("status-%s glyph meets 3:1 on plain surfaces", (s) => {
    expect(contrast(t[`status-${s}`]!, t["color-bg"]!)).toBeGreaterThanOrEqual(3);
    expect(contrast(t[`status-${s}`]!, t["color-bg-raised"]!)).toBeGreaterThanOrEqual(3);
  });

  it("every light token has a dark counterpart (no theme branch in components)", () => {
    const light = THEMES[0][1];
    const dark = THEMES[1][1];
    const colorKeys = Object.keys(light).filter((k) => /^(color|status)-/.test(k));
    for (const k of colorKeys) {
      expect(dark[k], `dark value for --${k}`).toBeDefined();
    }
  });
});
