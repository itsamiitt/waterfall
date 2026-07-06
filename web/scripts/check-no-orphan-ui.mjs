// check-no-orphan-ui.mjs — P11 acceptance #1 (the phase exit, doc 12 §P11).
//
// Rule (doc 09 §14, doc 17 extended): every /v1/admin endpoint path a feature screen calls
// MUST be a documented endpoint. UI surface ⊆ doc-04 endpoints. A referenced path with no
// documented match is an ORPHAN PANEL and fails the build.
//
// How it works:
//   1. Parse the METHOD | PATH table rows from docs/waterfall-dashboard/04-api-contracts.md.
//      Paths are /v1/admin-relative (the api client prepends the base), e.g. `/cost/summary`,
//      `/providers/{id}/stats`. Template params {x} normalize to a wildcard segment.
//   2. Walk web/src/features/**/*.{ts,tsx} (excluding *.test.*) for calls to the api client
//      helpers get/post/put/patch/del/api. The first string/template argument is the path.
//      `${…}` path params and query builders normalize away; a literal `?query` is dropped.
//   3. Each referenced path must segment-match a documented path (a wildcard matches anything).
//      Unmatched references are printed as orphans and the process exits non-zero.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = join(here, "..");
const repoRoot = join(webRoot, "..");
const DOC = join(repoRoot, "docs", "waterfall-dashboard", "04-api-contracts.md");
const FEATURES = join(webRoot, "src", "features");

// ---- 1. documented endpoints -------------------------------------------------

/** Normalize a path to comparable segments: strip query, collapse {param} -> {}, trim slash. */
function normalizeDocPath(raw) {
  let s = raw.trim();
  const q = s.indexOf("?");
  if (q >= 0) s = s.slice(0, q);
  s = s.replace(/\{[^}]*\}/g, "{}");
  s = s.replace(/\/+/g, "/").replace(/\/$/, "");
  return s;
}

function parseDocEndpoints(text) {
  const rows = [];
  const rowRe = /^\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*`([^`]+)`/gm;
  let m;
  while ((m = rowRe.exec(text)) !== null) {
    const path = m[2].trim();
    if (!path.startsWith("/")) continue;
    rows.push({ method: m[1], raw: path, norm: normalizeDocPath(path) });
  }
  return rows;
}

// ---- 2. referenced paths in feature code ------------------------------------

function listFeatureFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    const st = statSync(full);
    if (st.isDirectory()) out.push(...listFeatureFiles(full));
    else if (/\.(ts|tsx)$/.test(name) && !/\.test\.(ts|tsx)$/.test(name)) out.push(full);
  }
  return out;
}

/** Replace `${…}` interpolations: query builders drop out, path params become one wildcard. */
function stripInterpolations(s) {
  let out = "";
  for (let i = 0; i < s.length; ) {
    if (s[i] === "$" && s[i + 1] === "{") {
      let depth = 1;
      let j = i + 2;
      for (; j < s.length && depth > 0; j++) {
        if (s[j] === "{") depth++;
        else if (s[j] === "}") depth--;
      }
      const inner = s.slice(i + 2, j - 1);
      // A query-string builder (listQuery(...), buildXQuery(...)) or an inline `?` contributes
      // no path segment; a value interpolation (id, encodeURIComponent(id), action) is one segment.
      out += /query/i.test(inner) || inner.includes("?") ? "" : "{}";
      i = j;
    } else {
      out += s[i];
      i++;
    }
  }
  return out;
}

function normalizeCodePath(raw) {
  let s = stripInterpolations(raw);
  const q = s.indexOf("?");
  if (q >= 0) s = s.slice(0, q);
  s = s.replace(/\/+/g, "/").replace(/\/$/, "");
  return s;
}

const CALL_RE = /(?<![\w.$])(get|post|put|patch|del|api)(?![\w])[^\n(]*\(\s*(["'`])(\/[^"'`]*)\2/g;

function referencesIn(file, text) {
  const refs = [];
  let m;
  CALL_RE.lastIndex = 0;
  while ((m = CALL_RE.exec(text)) !== null) {
    const rawPath = m[3];
    const norm = normalizeCodePath(rawPath);
    if (norm === "" || norm === "/") continue;
    const line = text.slice(0, m.index).split("\n").length;
    refs.push({ file, line, fn: m[1], rawPath, norm });
  }
  return refs;
}

// ---- 3. match ----------------------------------------------------------------

function segmentsMatch(codeNorm, docNorm) {
  const a = codeNorm.split("/");
  const b = docNorm.split("/");
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] === b[i]) continue;
    if (a[i] === "{}" || b[i] === "{}") continue; // wildcard on either side
    return false;
  }
  return true;
}

// ---- run ---------------------------------------------------------------------

function main() {
  const docText = readFileSync(DOC, "utf8");
  const endpoints = parseDocEndpoints(docText);
  if (endpoints.length === 0) {
    console.error("check:orphan FAILED — parsed 0 endpoints from 04-api-contracts.md");
    process.exit(1);
  }
  const docNorms = [...new Set(endpoints.map((e) => e.norm))];

  const files = listFeatureFiles(FEATURES);
  const refs = [];
  for (const f of files) refs.push(...referencesIn(f, readFileSync(f, "utf8")));

  const orphans = refs.filter((r) => !docNorms.some((d) => segmentsMatch(r.norm, d)));

  const uniqueRefs = new Set(refs.map((r) => r.norm));
  console.log(
    `check:orphan — ${endpoints.length} documented endpoints, ` +
      `${refs.length} api references across ${files.length} feature files ` +
      `(${uniqueRefs.size} distinct paths).`,
  );

  if (orphans.length > 0) {
    console.error(`\nORPHAN UI — ${orphans.length} referenced path(s) not documented in 04-api-contracts.md:`);
    for (const o of orphans) {
      console.error(`  - ${o.fn}("${o.rawPath}")  ->  ${o.norm}   [${relative(repoRoot, o.file)}:${o.line}]`);
    }
    process.exit(1);
  }
  console.log("check:orphan OK — 0 orphan panels; every UI endpoint maps to a documented /v1/admin path.");
}

main();
