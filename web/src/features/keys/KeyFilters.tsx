// features/keys — filter bar. The dropdowns map 1:1 to the doc 04 §2.4 whitelist
// (status, health, region, environment, tag, rotation_group, imported_batch_id, pool_id) — there
// is deliberately NO free-text q (doc 09 §3.1: unknown param → 400 invalid_filter; free-text key
// lookup goes through the top-bar search). Screen → endpoint: these feed GET /providers/{id}/keys.
import { Button, Input, Select, type SelectOption } from "../../design/primitives";
import { KEY_STATUSES } from "../../lib/status";
import type { KeyFilter } from "./types";

const statusOpts: SelectOption[] = KEY_STATUSES.map((s) => ({ value: s, label: s }));
const healthOpts: SelectOption[] = [
  { value: "ok", label: "ok" },
  { value: "warn", label: "warn" },
  { value: "err", label: "err" },
  { value: "unknown", label: "unknown" },
];

export function KeyFilters({
  filter,
  onChange,
}: {
  filter: KeyFilter;
  onChange: (f: KeyFilter) => void;
}) {
  const set = (patch: Partial<KeyFilter>) => onChange({ ...filter, ...patch });
  const active = Object.values(filter).some((v) => v !== undefined && (!Array.isArray(v) || v.length));

  return (
    <div className="filter-bar" role="search">
      <Select
        label="Status"
        options={statusOpts}
        value={filter.status?.[0] ?? ""}
        placeholder="any"
        onChange={(v) => set({ status: v ? [v] : undefined })}
      />
      <Select
        label="Health"
        options={healthOpts}
        value={filter.health?.[0] ?? ""}
        placeholder="any"
        onChange={(v) => set({ health: v ? [v] : undefined })}
      />
      <Input label="Region" value={filter.region ?? ""} onChange={(v) => set({ region: v || undefined })} />
      <Input label="Env" value={filter.environment ?? ""} onChange={(v) => set({ environment: v || undefined })} />
      <Input label="Pool id" value={filter.pool_id ?? ""} mono onChange={(v) => set({ pool_id: v || undefined })} />
      <Input
        label="Batch id"
        value={filter.imported_batch_id ?? ""}
        mono
        onChange={(v) => set({ imported_batch_id: v || undefined })}
      />
      <Input label="Tag" value={filter.tag ?? ""} onChange={(v) => set({ tag: v || undefined })} />
      {active ? (
        <Button size="sm" onClick={() => onChange({})}>
          Clear filters
        </Button>
      ) : null}
    </div>
  );
}
