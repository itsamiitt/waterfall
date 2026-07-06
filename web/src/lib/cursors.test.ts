import { describe, expect, it } from "vitest";
import {
  clampLimit,
  flattenPages,
  getNextPageParam,
  LIMIT_CAP,
  listQuery,
  type Page,
} from "./cursors";

describe("cursor pagination helpers (doc 04 §1.4)", () => {
  it("getNextPageParam threads the opaque cursor and stops on null", () => {
    const page: Page<number> = { items: [1], next_cursor: "eyJrIjoiYSIsImlkIjoiYi0xNyJ9" };
    expect(getNextPageParam(page)).toBe("eyJrIjoiYSIsImlkIjoiYi0xNyJ9");
    expect(getNextPageParam({ items: [1], next_cursor: null })).toBeUndefined();
  });

  it("flattenPages concatenates in order and tolerates undefined", () => {
    const pages: Page<string>[] = [
      { items: ["a", "b"], next_cursor: "c1" },
      { items: ["c"], next_cursor: null },
    ];
    expect(flattenPages(pages)).toEqual(["a", "b", "c"]);
    expect(flattenPages(undefined)).toEqual([]);
  });

  it("clampLimit passes valid limits through and throws in dev on violations", () => {
    expect(clampLimit(50)).toBe(50);
    expect(clampLimit(LIMIT_CAP)).toBe(LIMIT_CAP);
    // vitest runs with DEV=true: out-of-range is a programming error, not a silent clamp
    expect(() => clampLimit(LIMIT_CAP + 1)).toThrow(/doc 04/);
    expect(() => clampLimit(0)).toThrow();
    expect(() => clampLimit(2.5)).toThrow();
  });

  it("listQuery ORs repeated params, ANDs distinct ones, and appends limit + cursor", () => {
    const q = listQuery(
      { status: ["active", "paused"], provider_id: "hunter", empty: undefined },
      { limit: 100, cursor: "abc" },
    );
    expect(q).toBe("?status=active&status=paused&provider_id=hunter&limit=100&cursor=abc");
  });

  it("listQuery returns an empty string when nothing is set", () => {
    expect(listQuery({})).toBe("");
  });
});
