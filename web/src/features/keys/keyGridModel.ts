// features/keys/keyGridModel.ts — pure row-model helpers for the virtualized grid (doc 09 §3.1,
// P9 acceptance #1). Kept separate from KeyGrid.tsx so the aria-rowcount/rowindex math and the
// fetch-ahead trigger are unit-testable in the node env.
import { flattenPages, type Page } from "../../lib/cursors";
import type { ProviderKey } from "./types";

export function flattenKeyPages(pages: readonly Page<ProviderKey>[] | undefined): ProviderKey[] {
  return flattenPages(pages);
}

/** aria-rowcount is the SERVER total (the preview {matched} count) once known, else the loaded
 * row count so the grid always announces a coherent size (doc 08 §9). */
export function gridAriaRowCount(serverTotal: number | undefined, loadedCount: number): number {
  return serverTotal !== undefined && serverTotal >= loadedCount ? serverTotal : loadedCount;
}

/** aria-rowindex is 1-based and header-inclusive: the first body row is 2 (doc 08 §9). */
export function ariaRowIndex(zeroBasedRow: number): number {
  return zeroBasedRow + 2;
}

/** Fetch-ahead trigger for infinite virtualization: fire when scrolled within thresholdPx of the
 * bottom, more pages exist, and no fetch is already in flight. */
export function shouldFetchNext(o: {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
  hasNextPage: boolean;
  isFetching: boolean;
  thresholdPx?: number;
}): boolean {
  if (!o.hasNextPage || o.isFetching) return false;
  const threshold = o.thresholdPx ?? 400;
  return o.scrollHeight - o.scrollTop - o.clientHeight <= threshold;
}
