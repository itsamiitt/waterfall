// features/keys — the 4-step import wizard (doc 09 §3.2). Source (csv/xlsx/json/paste) → map
// columns → validate preview → progress. Pure logic in importWizard.ts (parse/map/validate/step
// gates); this is the shell. Screen → endpoint: GET /providers (provider selector),
// POST /providers/{id}/keys/import (X-MFA-Code) → 202 {job_id}, GET /key-imports/{job_id} + SSE
// `import` for live progress with per-row errors. xlsx: see OI note below.
import { useMemo, useState } from "react";
import { useSseTopics } from "../../api/sse";
import { Button, EmptyState, Input, Select, type SelectOption } from "../../design/primitives";
import { isApiError } from "../../api/client";
import { flattenPages } from "../../lib/cursors";
import { formatCount } from "../../lib/format";
import { useProviders } from "../providers/api";
import { useImportKeys, useImportProgress } from "./api";
import {
  KEY_FIELDS,
  buildCanonicalCsv,
  canAdvance,
  parseCsv,
  parseJson,
  suggestMapping,
  validateParsed,
  type ColumnTarget,
  type SourceKind,
  type WizardState,
} from "./importWizard";

const SOURCES: SourceKind[] = ["csv", "xlsx", "json", "paste"];
const TARGET_OPTS: SelectOption[] = [
  { value: "ignore", label: "ignore" },
  ...KEY_FIELDS.map((f) => ({ value: f, label: f })),
];

export default function ImportWizard() {
  useSseTopics(["import"]);
  const provQ = useProviders({}, "priority");
  const providers = flattenPages(provQ.data?.pages);
  const provOpts: SelectOption[] = providers.map((p) => ({ value: p.id, label: p.display_name }));

  const [state, setState] = useState<WizardState>({
    step: 1,
    providerId: "",
    source: "paste",
    text: "",
    fileName: null,
    parsed: null,
    mapping: {},
  });
  const [skipInvalid, setSkipInvalid] = useState(true);
  const [mfa, setMfa] = useState("");
  const [jobId, setJobId] = useState<string | null>(null);

  const importMut = useImportKeys(state.providerId);
  const patch = (p: Partial<WizardState>) => setState((s) => ({ ...s, ...p }));

  const preview = useMemo(
    () => (state.parsed ? validateParsed(state.parsed, state.mapping) : null),
    [state.parsed, state.mapping],
  );

  function parseText(text: string, source: SourceKind) {
    try {
      const parsed = source === "json" ? parseJson(text) : parseCsv(text);
      patch({ text, parsed, mapping: suggestMapping(parsed.headers) });
    } catch {
      patch({ text, parsed: null, mapping: {} });
    }
  }

  async function onFile(file: File | undefined) {
    if (!file) return;
    if (state.source === "xlsx") {
      patch({ fileName: file.name }); // xlsx parsed server-side (no client lib — ADR-0016)
      return;
    }
    const text = await file.text();
    patch({ fileName: file.name });
    parseText(text, state.source);
  }

  function startImport() {
    const data = state.parsed ? buildCanonicalCsv(state.parsed, state.mapping) : state.text;
    importMut.mutate(
      { data, mfaCode: mfa || undefined },
      {
        onSuccess: (r) => {
          setJobId(r.job_id);
          patch({ step: 4 });
        },
      },
    );
  }

  const canNext = canAdvance(state);

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Import keys</h1>
        <span className="page-header-meta">Step {state.step} of 4</span>
      </div>

      {state.step === 1 ? (
        <div className="section" style={{ maxWidth: 560 }}>
          <Select
            label="Provider"
            options={provOpts}
            value={state.providerId}
            placeholder={provQ.isPending ? "loading…" : "select provider"}
            onChange={(v) => patch({ providerId: v })}
          />
          <div className="radio-row" role="radiogroup" aria-label="Source format">
            {SOURCES.map((s) => (
              <label key={s} className="radio-chip">
                <input
                  type="radio"
                  name="source"
                  checked={state.source === s}
                  onChange={() => patch({ source: s, parsed: null, mapping: {}, text: "", fileName: null })}
                />
                {s}
              </label>
            ))}
          </div>
          {state.source === "paste" ? (
            <label className="p-field">
              <span className="p-field-label">Paste rows (first line = headers)</span>
              <textarea
                className="p-input"
                rows={8}
                value={state.text}
                onChange={(e) => parseText(e.currentTarget.value, "paste")}
              />
            </label>
          ) : (
            <label className="p-field">
              <span className="p-field-label">Choose {state.source.toUpperCase()} file</span>
              <input type="file" onChange={(e) => void onFile(e.currentTarget.files?.[0])} />
            </label>
          )}
          {state.source === "xlsx" ? (
            <p className="p-field-description">
              xlsx is parsed server-side at import; column mapping preview is skipped (OI-P9-xlsx).
            </p>
          ) : null}
          <p className="p-field-description">Caps: 25 MiB · 50,000 rows. Secrets are write-only — never viewable after import.</p>
        </div>
      ) : null}

      {state.step === 2 ? (
        <div className="section" style={{ maxWidth: 560 }}>
          {state.parsed ? (
            <>
              <p>Detected {state.parsed.headers.length} columns in {formatCount(state.parsed.rows.length)} rows.</p>
              <div className="form-grid">
                {state.parsed.headers.map((h) => (
                  <Select
                    key={h}
                    label={`"${h}"`}
                    options={TARGET_OPTS}
                    value={state.mapping[h] ?? "ignore"}
                    onChange={(v) => patch({ mapping: { ...state.mapping, [h]: v as ColumnTarget } })}
                  />
                ))}
              </div>
            </>
          ) : (
            <EmptyState variant="zero-data" title="Nothing parsed — go back and provide data" />
          )}
        </div>
      ) : null}

      {state.step === 3 ? (
        <div className="section" style={{ maxWidth: 640 }}>
          {preview ? (
            <>
              <p>
                {formatCount(preview.valid + preview.issues.length)} rows parsed ·{" "}
                <strong>{formatCount(preview.valid)}</strong> valid ·{" "}
                <strong>{formatCount(preview.issues.length)}</strong> flagged
              </p>
              {preview.issues.length > 0 ? (
                <table className="p-table">
                  <thead><tr><th scope="col">Row</th><th scope="col">Issue</th></tr></thead>
                  <tbody>
                    {preview.issues.slice(0, 50).map((iss, i) => (
                      <tr key={i}><td>{iss.row}</td><td>{iss.message}</td></tr>
                    ))}
                  </tbody>
                </table>
              ) : null}
            </>
          ) : (
            <p>xlsx will be validated server-side after upload.</p>
          )}
          <label className="radio-chip">
            <input type="checkbox" checked={skipInvalid} onChange={(e) => setSkipInvalid(e.currentTarget.checked)} />
            skip invalid rows
          </label>
          <Input label="MFA code" value={mfa} onChange={setMfa} mono description="Required to import (doc 05 §5.4)." />
          {importMut.isError && isApiError(importMut.error) && importMut.error.code === "mfa_required" ? (
            <p className="p-field-error">Enter a valid MFA code to continue.</p>
          ) : null}
        </div>
      ) : null}

      {state.step === 4 ? <ImportProgress jobId={jobId} /> : null}

      <div className="action-bar">
        {state.step > 1 && state.step < 4 ? (
          <Button onClick={() => patch({ step: (state.step - 1) as WizardState["step"] })}>Back</Button>
        ) : null}
        {state.step < 3 ? (
          <Button variant="primary" disabled={!canNext} onClick={() => patch({ step: (state.step + 1) as WizardState["step"] })}>
            Continue
          </Button>
        ) : null}
        {state.step === 3 ? (
          <Button variant="primary" loading={importMut.isPending} onClick={startImport}>
            Start import
          </Button>
        ) : null}
      </div>
    </>
  );
}

function ImportProgress({ jobId }: { jobId: string | null }) {
  const q = useImportProgress(jobId);
  const p = q.data;
  if (!p) return <div className="skeleton" style={{ height: 120 }} aria-busy="true" />;
  const done = p.succeeded + p.failed;
  const pct = p.total > 0 ? Math.min(100, (done / p.total) * 100) : 0;
  return (
    <div className="section" style={{ maxWidth: 640 }}>
      <div className="detail-meta">
        <span>batch <code>{p.job_id.slice(0, 12)}</code></span>
        <span>status <strong>{p.status}</strong></span>
      </div>
      <div className="progress-track" role="progressbar" aria-valuenow={done} aria-valuemin={0} aria-valuemax={p.total} aria-label="Import progress">
        <div className="progress-fill" style={{ width: `${pct}%` }} />
      </div>
      <div className="detail-meta">
        <span>{formatCount(done)} / {formatCount(p.total)}</span>
        <span>succeeded {formatCount(p.succeeded)}</span>
        <span>failed {formatCount(p.failed)}</span>
      </div>
      {p.errors && p.errors.length > 0 ? (
        <table className="p-table">
          <thead><tr><th scope="col">Row</th><th scope="col">Code</th><th scope="col">Message</th></tr></thead>
          <tbody>
            {p.errors.map((e, i) => (
              <tr key={i}><td>{e.row ?? "—"}</td><td><code>{e.code}</code></td><td>{e.message}</td></tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </div>
  );
}
