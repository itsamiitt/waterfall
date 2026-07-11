// features/airesearch unit tests: the Dossier headline extractor is defensive against arbitrary JSON
// (doc 06 — the Dossier is a composite document, not a fixed shape) and never throws on bad input.
import { describe, expect, it } from "vitest";
import { dossierHeadline } from "./logic";

describe("airesearch dossierHeadline", () => {
  it("returns null for non-objects", () => {
    expect(dossierHeadline(null)).toBeNull();
    expect(dossierHeadline("x")).toBeNull();
    expect(dossierHeadline(42)).toBeNull();
    expect(dossierHeadline(undefined)).toBeNull();
  });

  it("prefers company_profile.name", () => {
    expect(dossierHeadline({ company_profile: { name: "Acme" }, subject_key: "acme.com" })).toBe("Acme");
  });

  it("falls back to subject_key when there is no company name", () => {
    expect(dossierHeadline({ subject_key: "acme.com" })).toBe("acme.com");
    expect(dossierHeadline({ company_profile: {}, subject_key: "acme.com" })).toBe("acme.com");
  });

  it("returns null when neither a name nor a subject key is present", () => {
    expect(dossierHeadline({ firmographics: {} })).toBeNull();
  });
});
