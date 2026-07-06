// Virtualized grid row-model tests (P9 acceptance #1): aria-rowcount from server totals,
// header-inclusive aria-rowindex, and the fetch-ahead trigger.
import { describe, expect, it } from "vitest";
import { ariaRowIndex, flattenKeyPages, gridAriaRowCount, shouldFetchNext } from "./keyGridModel";
import type { Page } from "../../lib/cursors";
import type { ProviderKey } from "./types";

describe("gridAriaRowCount", () => {
  it("uses the server total when known and ≥ loaded", () => {
    expect(gridAriaRowCount(4213, 100)).toBe(4213);
  });
  it("falls back to loaded count when the total is unknown", () => {
    expect(gridAriaRowCount(undefined, 100)).toBe(100);
  });
  it("never announces fewer than the loaded rows", () => {
    expect(gridAriaRowCount(50, 100)).toBe(100);
  });
});

describe("ariaRowIndex", () => {
  it("is 1-based and header-inclusive (first body row = 2)", () => {
    expect(ariaRowIndex(0)).toBe(2);
    expect(ariaRowIndex(510)).toBe(512);
  });
});

describe("shouldFetchNext", () => {
  const base = { scrollTop: 900, scrollHeight: 1000, clientHeight: 100, hasNextPage: true, isFetching: false };
  it("fires within the threshold of the bottom", () => {
    expect(shouldFetchNext(base)).toBe(true);
  });
  it("does not fire far from the bottom", () => {
    expect(shouldFetchNext({ ...base, scrollTop: 0 })).toBe(false);
  });
  it("does not fire while already fetching", () => {
    expect(shouldFetchNext({ ...base, isFetching: true })).toBe(false);
  });
  it("does not fire when there is no next page", () => {
    expect(shouldFetchNext({ ...base, hasNextPage: false })).toBe(false);
  });
});

describe("flattenKeyPages", () => {
  it("concatenates page items in order", () => {
    const pages: Page<ProviderKey>[] = [
      { items: [{ id: "1" } as ProviderKey], next_cursor: "c" },
      { items: [{ id: "2" } as ProviderKey], next_cursor: null },
    ];
    expect(flattenKeyPages(pages).map((k) => k.id)).toEqual(["1", "2"]);
    expect(flattenKeyPages(undefined)).toEqual([]);
  });
});
