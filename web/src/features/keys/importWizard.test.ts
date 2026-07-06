// Import wizard step-validation + parsing tests (P9: import wizard step validation).
import { describe, expect, it } from "vitest";
import {
  buildCanonicalCsv,
  canAdvance,
  mappingHasRequired,
  parseCsv,
  parseJson,
  suggestMapping,
  validateParsed,
  type WizardState,
} from "./importWizard";

const CSV = 'name,api_key,region\nhunter-08,hk_live_aa11,us\nhunter-09,,eu\n';

describe("parseCsv", () => {
  it("splits headers and non-empty data rows", () => {
    const p = parseCsv(CSV);
    expect(p.headers).toEqual(["name", "api_key", "region"]);
    expect(p.rows).toHaveLength(2);
    expect(p.rows[0]).toEqual(["hunter-08", "hk_live_aa11", "us"]);
  });
  it("handles quoted fields with embedded commas", () => {
    const p = parseCsv('label,secret\n"a,b",s1\n');
    expect(p.rows[0]).toEqual(["a,b", "s1"]);
  });
});

describe("parseJson", () => {
  it("builds headers from the union of object keys", () => {
    const p = parseJson('[{"label":"a","secret":"s"},{"label":"b","region":"eu"}]');
    expect(p.headers).toEqual(["label", "secret", "region"]);
    expect(p.rows[1]).toEqual(["b", "", "eu"]);
  });
});

describe("suggestMapping + required", () => {
  it("maps synonyms (name→label, api_key→secret)", () => {
    const m = suggestMapping(["name", "api_key", "region", "team"]);
    expect(m.name).toBe("label");
    expect(m.api_key).toBe("secret");
    expect(m.region).toBe("region");
    expect(m.team).toBe("ignore");
  });
  it("requires a secret column", () => {
    expect(mappingHasRequired(suggestMapping(["name", "api_key"]))).toBe(true);
    expect(mappingHasRequired(suggestMapping(["name", "region"]))).toBe(false);
  });
});

describe("validateParsed", () => {
  it("flags rows with an empty secret", () => {
    const parsed = parseCsv(CSV);
    const { valid, issues } = validateParsed(parsed, suggestMapping(parsed.headers));
    expect(valid).toBe(1);
    expect(issues).toHaveLength(1);
    expect(issues[0]!.message).toContain("secret");
    expect(issues[0]!.row).toBe(3); // header row 1 + 2nd data row
  });
});

describe("buildCanonicalCsv", () => {
  it("re-serializes onto canonical columns applying the mapping", () => {
    const parsed = parseCsv(CSV);
    const csv = buildCanonicalCsv(parsed, suggestMapping(parsed.headers));
    const lines = csv.split("\n");
    expect(lines[0]).toBe("label,secret,region");
    expect(lines[1]).toBe("hunter-08,hk_live_aa11,us");
  });
});

describe("canAdvance step gates", () => {
  const base: WizardState = {
    step: 1, providerId: "", source: "paste", text: "", fileName: null, parsed: null, mapping: {},
  };
  it("step 1 needs a provider and content", () => {
    expect(canAdvance(base)).toBe(false);
    expect(canAdvance({ ...base, providerId: "hunter", text: "a,b" })).toBe(true);
  });
  it("step 2 needs a parsed file with a secret mapping (xlsx exempt)", () => {
    const parsed = parseCsv(CSV);
    expect(canAdvance({ ...base, step: 2, providerId: "hunter", parsed, mapping: suggestMapping(parsed.headers) })).toBe(true);
    expect(canAdvance({ ...base, step: 2, providerId: "hunter", parsed, mapping: {} })).toBe(false);
    expect(canAdvance({ ...base, step: 2, providerId: "hunter", source: "xlsx", fileName: "k.xlsx" })).toBe(true);
  });
  it("step 3 always allows starting the import", () => {
    expect(canAdvance({ ...base, step: 3 })).toBe(true);
  });
});
