// features/keys/importWizard.ts — pure logic for the 4-step import wizard (doc 09 §3.2). The UI
// (ImportWizard.tsx) is a thin shell over these functions so step validation, CSV/JSON parsing,
// column mapping and canonicalization are all unit-testable in the node test env. The server
// still owns authoritative validation (fingerprint dedupe, region vocab) at import time (§4.4).

/** Server-canonical key-import columns; the mapping normalizes arbitrary headers onto these. */
export const KEY_FIELDS = [
  "label",
  "secret",
  "region",
  "environment",
  "daily_limit",
  "monthly_limit",
  "rpm_limit",
  "weight",
  "priority",
  "expires_at",
  "tags",
] as const;
export type KeyField = (typeof KEY_FIELDS)[number];
export type ColumnTarget = KeyField | "ignore";
export type Mapping = Record<string, ColumnTarget>;

export type SourceKind = "csv" | "xlsx" | "json" | "paste";

export interface ParsedFile {
  headers: string[];
  rows: string[][];
}

/** Minimal RFC-4180-ish CSV: comma delimiter, double-quote escaping, CRLF/LF rows. No deps. */
export function parseCsv(text: string): ParsedFile {
  const lines = splitRecords(text);
  if (lines.length === 0) return { headers: [], rows: [] };
  const headers = lines[0]!;
  return { headers, rows: lines.slice(1).filter((r) => r.some((c) => c !== "")) };
}

function splitRecords(text: string): string[][] {
  const records: string[][] = [];
  let field = "";
  let record: string[] = [];
  let inQuotes = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!;
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else inQuotes = false;
      } else field += ch;
    } else if (ch === '"') {
      inQuotes = true;
    } else if (ch === ",") {
      record.push(field);
      field = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      record.push(field);
      records.push(record);
      field = "";
      record = [];
    } else field += ch;
  }
  if (field !== "" || record.length > 0) {
    record.push(field);
    records.push(record);
  }
  return records;
}

/** JSON import: array of objects → headers = union of keys, rows aligned to headers. */
export function parseJson(text: string): ParsedFile {
  const data = JSON.parse(text) as unknown;
  if (!Array.isArray(data)) throw new Error("JSON import must be an array of objects");
  const headerSet: string[] = [];
  for (const item of data) {
    if (item && typeof item === "object") {
      for (const k of Object.keys(item as object)) if (!headerSet.includes(k)) headerSet.push(k);
    }
  }
  const rows = data.map((item) =>
    headerSet.map((h) => {
      const v = (item as Record<string, unknown>)?.[h];
      return v === null || v === undefined ? "" : String(v);
    }),
  );
  return { headers: headerSet, rows };
}

const SYNONYMS: Record<string, KeyField> = {
  name: "label",
  label: "label",
  key: "secret",
  api_key: "secret",
  apikey: "secret",
  secret: "secret",
  token: "secret",
  region: "region",
  env: "environment",
  environment: "environment",
  daily: "daily_limit",
  daily_limit: "daily_limit",
  monthly: "monthly_limit",
  monthly_limit: "monthly_limit",
  rpm: "rpm_limit",
  rpm_limit: "rpm_limit",
  weight: "weight",
  priority: "priority",
  expires: "expires_at",
  expires_at: "expires_at",
  tags: "tags",
};

/** Heuristic header → field mapping; unrecognized headers default to "ignore". */
export function suggestMapping(headers: readonly string[]): Mapping {
  const m: Mapping = {};
  for (const h of headers) m[h] = SYNONYMS[h.trim().toLowerCase()] ?? "ignore";
  return m;
}

/** secret is the only unconditionally-required column (no key without a secret). */
export function mappingHasRequired(mapping: Mapping): boolean {
  return Object.values(mapping).includes("secret");
}

export interface RowIssue {
  row: number;
  code: string;
  message: string;
}

/** Client-side structural preview (advisory). Fingerprint dedupe + region vocab are server-side
 * (doc 04 §4.4) and surface as per-row errors in step 4, not here. */
export function validateParsed(
  parsed: ParsedFile,
  mapping: Mapping,
): { valid: number; issues: RowIssue[] } {
  const secretIdx = parsed.headers.findIndex((h) => mapping[h] === "secret");
  const labelIdx = parsed.headers.findIndex((h) => mapping[h] === "label");
  const issues: RowIssue[] = [];
  let valid = 0;
  parsed.rows.forEach((row, i) => {
    const rowNo = i + 2; // 1-based incl. header row
    let ok = true;
    if (secretIdx < 0 || (row[secretIdx] ?? "").trim() === "") {
      issues.push({ row: rowNo, code: "validation_failed", message: "secret column empty" });
      ok = false;
    }
    if (labelIdx >= 0 && (row[labelIdx] ?? "").trim() === "") {
      issues.push({ row: rowNo, code: "validation_failed", message: "label empty" });
      ok = false;
    }
    if (ok) valid++;
  });
  return { valid, issues };
}

/** Re-serialize mapped rows into a canonical CSV the import endpoint understands, so the client
 * mapping is applied before upload (paste variant → POST body {format:"paste", data}). */
export function buildCanonicalCsv(parsed: ParsedFile, mapping: Mapping): string {
  const targets = KEY_FIELDS.filter((f) =>
    parsed.headers.some((h) => mapping[h] === f),
  );
  const colIndex = new Map<KeyField, number>();
  targets.forEach((t) => {
    colIndex.set(
      t,
      parsed.headers.findIndex((h) => mapping[h] === t),
    );
  });
  const esc = (v: string) => (/[",\n\r]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v);
  const lines = [targets.join(",")];
  for (const row of parsed.rows) {
    lines.push(targets.map((t) => esc(row[colIndex.get(t)!] ?? "")).join(","));
  }
  return lines.join("\n");
}

export type WizardStep = 1 | 2 | 3 | 4;

export interface WizardState {
  step: WizardStep;
  providerId: string;
  source: SourceKind;
  text: string;
  fileName: string | null;
  parsed: ParsedFile | null;
  mapping: Mapping;
}

/** Per-step gate for the "Continue" button (P9: import wizard step validation). */
export function canAdvance(state: WizardState): boolean {
  switch (state.step) {
    case 1:
      return state.providerId !== "" && (state.source === "xlsx" ? state.fileName !== null : hasContent(state));
    case 2:
      // xlsx is parsed server-side (no client lib), so mapping is skipped for it.
      return state.source === "xlsx" || (state.parsed !== null && mappingHasRequired(state.mapping));
    case 3:
      return true; // start import always allowed; skip_invalid governs invalid rows
    default:
      return false;
  }
}

function hasContent(state: WizardState): boolean {
  return state.source === "paste" ? state.text.trim() !== "" : state.fileName !== null || state.text.trim() !== "";
}
