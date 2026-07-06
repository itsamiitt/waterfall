// features/keys/keyExport.ts — client-side "Export view" (doc 09 §3.1). There is NO keys-export
// endpoint: this serializes EXACTLY the rows already in the grid's useInfiniteQuery cache
// (cursor-bounded WYSIWYG of the active filter+sort), buffered in memory, fetching no extra
// pages, calling no endpoint. Cells starting =, +, -, @ pass through the shared formula escaper
// (doc 05 §7 "key-grid export" threat). No secret is ever exported — there is none in the DTO.
import type { ProviderKey } from "./types";

const COLUMNS: { key: keyof ProviderKey; header: string }[] = [
  { key: "label", header: "label" },
  { key: "secret_last4", header: "secret_last4" },
  { key: "status", header: "status" },
  { key: "health", header: "health" },
  { key: "pool", header: "pool" },
  { key: "region", header: "region" },
  { key: "environment", header: "environment" },
  { key: "credits_remaining", header: "credits_remaining" },
  { key: "usage_today", header: "usage_today" },
  { key: "success_ewma", header: "success_ewma" },
  { key: "latency_ewma_ms", header: "latency_ewma_ms" },
  { key: "expires_at", header: "expires_at" },
  { key: "last_used_at", header: "last_used_at" },
];

/** Prefix a leading formula sigil with an apostrophe, then CSV-quote (doc 05 §7). */
export function escapeCell(value: unknown): string {
  let s = value === null || value === undefined ? "" : String(value);
  if (/^[=+\-@]/.test(s)) s = `'${s}`;
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/** Serialize the already-loaded rows to a CSV string (no network). */
export function keysToCsv(rows: readonly ProviderKey[]): string {
  const lines = [COLUMNS.map((c) => c.header).join(",")];
  for (const r of rows) {
    lines.push(COLUMNS.map((c) => escapeCell(r[c.key])).join(","));
  }
  return lines.join("\n");
}

/** Filename embeds provider + a filter summary + row count so a partial export is self-evident. */
export function exportFilename(providerId: string, filterSummary: string, rowCount: number): string {
  const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "");
  const suffix = filterSummary ? `_${filterSummary}` : "";
  return `keys_${providerId}${suffix}_${rowCount}rows_${stamp}.csv`;
}

/** Trigger a buffered Blob download (browser-only; guarded for the node test env). */
export function downloadCsv(filename: string, csv: string): void {
  if (typeof document === "undefined" || typeof URL.createObjectURL !== "function") return;
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
