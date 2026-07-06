// lib/cursors.ts — helpers for the opaque cursor envelope of doc 04 §1.4. Cursors are opaque
// base64url blobs produced by the server's dash/db codec: the client NEVER constructs or
// parses one — these helpers only thread `next_cursor` back as `?cursor=`.

/** Uniform list envelope (doc 04 §1.4): `next_cursor` is null on the last page. */
export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}

/** Server hard cap on `limit` (doc 04 §1.4). Exceeding it is a client programming error. */
export const LIMIT_CAP = 200;
export const LIMIT_DEFAULT = 50;

/** Clamp a page size to the server's bounds. Out-of-range limits are a programming error
 * surfaced in dev (doc 08 §4), not a 400 discovered in production. */
export function clampLimit(limit: number): number {
  if (!Number.isInteger(limit) || limit < 1 || limit > LIMIT_CAP) {
    if (import.meta.env?.DEV) {
      throw new Error(`limit ${limit} outside 1..${LIMIT_CAP} (doc 04 §1.4)`);
    }
    return Math.min(Math.max(Math.trunc(limit) || LIMIT_DEFAULT, 1), LIMIT_CAP);
  }
  return limit;
}

/** `getNextPageParam` for useInfiniteQuery: null → undefined (stop paginating). */
export function getNextPageParam<T>(lastPage: Page<T>): string | undefined {
  return lastPage.next_cursor ?? undefined;
}

/** Initial page param for useInfiniteQuery (no cursor on the first request). */
export const initialPageParam: string | undefined = undefined;

/** Flatten useInfiniteQuery pages into one row array for grids. */
export function flattenPages<T>(pages: readonly Page<T>[] | undefined): T[] {
  if (!pages) return [];
  return pages.flatMap((p) => p.items);
}

/** Build a list query string: filters (repeatable params OR their values, doc 04 §1.5),
 * bounded limit, and the opaque cursor. */
export function listQuery(
  params: Record<string, string | number | readonly string[] | undefined>,
  opts?: { limit?: number; cursor?: string },
): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const v of value) q.append(key, v);
    } else {
      q.set(key, String(value));
    }
  }
  if (opts?.limit !== undefined) q.set("limit", String(clampLimit(opts.limit)));
  if (opts?.cursor) q.set("cursor", opts.cursor);
  const s = q.toString();
  return s ? `?${s}` : "";
}
