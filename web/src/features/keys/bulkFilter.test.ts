// Bulk-filter predicate tests (P9 acceptance #2): escalation sends the FILTER PREDICATE, not ids.
import { describe, expect, it } from "vitest";
import {
  buildBulkRequest,
  buildPreviewRequest,
  isApprovalGatedOp,
  resolveScope,
} from "./bulkFilter";
import type { KeyFilter } from "./types";

const filter: KeyFilter = { status: ["auth_failed"], imported_batch_id: "batch-1" };

describe("buildBulkRequest scope", () => {
  it("ids mode sends ids and NO filter", () => {
    const req = buildBulkRequest("hunter", filter, { mode: "ids", ids: ["a", "b"] }, "disable");
    expect(req.ids).toEqual(["a", "b"]);
    expect(req.filter).toBeUndefined();
    expect(req.provider_id).toBe("hunter");
    expect(req.op).toBe("disable");
  });

  it("filter mode sends the predicate (with provider_id) and NO ids", () => {
    const req = buildBulkRequest("hunter", filter, { mode: "filter" }, "delete");
    expect(req.ids).toBeUndefined();
    expect(req.filter).toEqual({ ...filter, provider_id: "hunter" });
    expect(req.op).toBe("delete");
  });

  it("carries reason and preview when provided", () => {
    const req = buildBulkRequest("hunter", filter, { mode: "filter" }, "disable", { reason: "x", preview: true });
    expect(req.reason).toBe("x");
    expect(req.preview).toBe(true);
  });
});

describe("buildPreviewRequest", () => {
  it("is a preview, filter-scoped, benign op — for the count only", () => {
    const req = buildPreviewRequest("hunter", filter);
    expect(req.preview).toBe(true);
    expect(req.filter).toEqual({ ...filter, provider_id: "hunter" });
    expect(req.ids).toBeUndefined();
    expect(req.op).toBe("disable");
  });
});

describe("resolveScope escalation", () => {
  it("allMatching → filter mode (predicate, not ids)", () => {
    expect(resolveScope(new Set(["a", "b"]), { allMatching: true })).toEqual({ mode: "filter" });
  });
  it("page selection → ids mode with the checked ids", () => {
    const scope = resolveScope(new Set(["a", "b"]), { allMatching: false });
    expect(scope.mode).toBe("ids");
    if (scope.mode === "ids") expect([...scope.ids].sort()).toEqual(["a", "b"]);
  });
});

describe("isApprovalGatedOp", () => {
  it("delete is approval-gated; the rest are not", () => {
    expect(isApprovalGatedOp("delete")).toBe(true);
    for (const op of ["enable", "disable", "pause", "rotate"] as const) {
      expect(isApprovalGatedOp(op)).toBe(false);
    }
  });
});
