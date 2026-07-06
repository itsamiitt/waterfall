// features/providers — generic config form (doc 09 §2.1 config tab). Screen → endpoint:
// GET /meta/enums drives every closed-vocabulary <select>; PATCH /providers/{id} saves (doc 04
// §2.3, partial update, auto Idempotency-Key via the P8 client). Numeric/URL fields are plain
// inputs; only closed vocabularies become selects — the closed-vocabulary doctrine (doc 04 §1.5).
import { useMemo, useState } from "react";
import { Button, Input, Select, type SelectOption } from "../../design/primitives";
import { INCLUSION_STATUSES } from "../../lib/status";
import { toast } from "../../app/toast";
import { useUpdateProvider } from "./api";
import type { MetaEnums, Provider } from "./types";

type FieldKind = "text" | "number" | "enum";
interface FieldSpec {
  key: keyof Provider;
  label: string;
  kind: FieldKind;
  /** meta/enums key for `kind:"enum"`. */
  enumKey?: string;
  /** static fallback vocabulary if meta/enums lacks the key. */
  fallback?: readonly string[];
}

const FIELDS: FieldSpec[] = [
  { key: "display_name", label: "Display name", kind: "text" },
  { key: "category", label: "Category", kind: "enum", enumKey: "provider_category" },
  { key: "base_url", label: "Base URL", kind: "text" },
  { key: "api_version", label: "API version", kind: "text" },
  { key: "auth_scheme", label: "Auth scheme", kind: "enum", enumKey: "auth_scheme" },
  { key: "auth_header", label: "Auth header", kind: "text" },
  { key: "timeout_ms", label: "Timeout (ms)", kind: "number" },
  { key: "rate_limit_rpm", label: "Rate limit (rpm)", kind: "number" },
  { key: "concurrency_limit", label: "Concurrency limit", kind: "number" },
  { key: "daily_limit", label: "Daily limit", kind: "number" },
  { key: "monthly_limit", label: "Monthly limit", kind: "number" },
  { key: "breaker_threshold", label: "Breaker threshold", kind: "number" },
  { key: "breaker_cooldown_s", label: "Breaker cooldown (s)", kind: "number" },
  { key: "priority", label: "Priority", kind: "number" },
  {
    key: "status",
    label: "Inclusion status",
    kind: "enum",
    enumKey: "provider_status",
    fallback: INCLUSION_STATUSES,
  },
  {
    key: "compliance_review_status",
    label: "Compliance review",
    kind: "enum",
    enumKey: "compliance_review_status",
  },
];

function toStr(v: unknown): string {
  return v === null || v === undefined ? "" : String(v);
}

export function ProviderConfigForm({ provider, enums }: { provider: Provider; enums: MetaEnums }) {
  const initial = useMemo(() => {
    const m: Record<string, string> = {};
    for (const f of FIELDS) m[f.key as string] = toStr(provider[f.key]);
    return m;
  }, [provider]);

  const [values, setValues] = useState<Record<string, string>>(initial);
  const update = useUpdateProvider(provider.id);
  const dirty = FIELDS.some((f) => values[f.key as string] !== initial[f.key as string]);

  function save() {
    const patchBody: Record<string, unknown> = {};
    for (const f of FIELDS) {
      const key = f.key as string;
      if (values[key] === initial[key]) continue;
      const raw = values[key] ?? "";
      if (f.kind === "number") {
        const n = Number(raw);
        patchBody[key] = raw === "" ? null : Number.isNaN(n) ? raw : n;
      } else {
        patchBody[key] = raw;
      }
    }
    update.mutate(patchBody, {
      onSuccess: () => toast.success("Provider updated"),
    });
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <div className="form-grid">
        {FIELDS.map((f) => {
          const key = f.key as string;
          const val = values[key] ?? "";
          const set = (v: string) => setValues((s) => ({ ...s, [key]: v }));
          if (f.kind === "enum") {
            const vocab = enums[f.enumKey ?? ""] ?? f.fallback ?? [];
            const opts: SelectOption[] = vocab.map((v) => ({ value: v, label: v }));
            return (
              <Select
                key={key}
                label={f.label}
                options={opts}
                value={(val as string) || ""}
                placeholder={vocab.length ? "select…" : "vocabulary unavailable"}
                disabled={vocab.length === 0}
                onChange={set}
              />
            );
          }
          return (
            <Input
              key={key}
              label={f.label}
              value={val}
              onChange={set}
              inputMode={f.kind === "number" ? "numeric" : undefined}
              mono={f.kind === "text"}
            />
          );
        })}
      </div>
      <div style={{ marginTop: "var(--space-5)" }}>
        <Button type="submit" variant="primary" loading={update.isPending} disabled={!dirty}>
          Save changes
        </Button>
      </div>
    </form>
  );
}
