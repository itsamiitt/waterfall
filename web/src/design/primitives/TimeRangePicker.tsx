import { useState } from "react";
import { Button } from "./Button";
import { Input } from "./Input";

export type TimeRes = "1m" | "1h" | "1d";
export type TimePreset = "15m" | "1h" | "24h" | "7d" | "30d";

export interface TimeRange {
  /** UTC timestamps (doc 04 §1.8); res is advisory — the server picks the serving rollup. */
  from: string;
  to: string;
  res: TimeRes;
}

export interface TimeRangePickerProps {
  value: TimeRange & { preset?: TimePreset | "custom" };
  onChange: (value: TimeRange & { preset: TimePreset | "custom" }) => void;
  presets?: readonly TimePreset[];
  /** Maximum window in seconds, bounded by the serving rollup's retention (doc 04 §1.8). */
  maxWindowS?: number;
  now?: () => Date;
}

const PRESET_SECONDS: Record<TimePreset, number> = {
  "15m": 15 * 60,
  "1h": 60 * 60,
  "24h": 24 * 60 * 60,
  "7d": 7 * 24 * 60 * 60,
  "30d": 30 * 24 * 60 * 60,
};

function presetRes(seconds: number): TimeRes {
  if (seconds <= 6 * 60 * 60) return "1m";
  if (seconds <= 7 * 24 * 60 * 60) return "1h";
  return "1d";
}

const iso = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, "Z");

export function TimeRangePicker({
  value,
  onChange,
  presets = ["15m", "1h", "24h", "7d", "30d"],
  maxWindowS,
  now = () => new Date(),
}: TimeRangePickerProps) {
  const [customOpen, setCustomOpen] = useState(value.preset === "custom");
  const [from, setFrom] = useState(value.from);
  const [to, setTo] = useState(value.to);
  const [error, setError] = useState<string | undefined>();

  function applyPreset(p: TimePreset) {
    const seconds = PRESET_SECONDS[p];
    const end = now();
    const start = new Date(end.getTime() - seconds * 1000);
    setCustomOpen(false);
    setError(undefined);
    onChange({ from: iso(start), to: iso(end), res: presetRes(seconds), preset: p });
  }

  function applyCustom() {
    // Client-side validation is a convenience; the server enforces window_out_of_range.
    const f = new Date(from);
    const t = new Date(to);
    if (Number.isNaN(f.getTime()) || Number.isNaN(t.getTime())) {
      setError("timestamps must be UTC, e.g. 2026-07-02T12:00:00Z");
      return;
    }
    if (f.getTime() >= t.getTime()) {
      setError("from must be before to");
      return;
    }
    const windowS = (t.getTime() - f.getTime()) / 1000;
    if (maxWindowS !== undefined && windowS > maxWindowS) {
      setError(`window exceeds the retention bound (${maxWindowS}s)`);
      return;
    }
    setError(undefined);
    onChange({ from: iso(f), to: iso(t), res: presetRes(windowS), preset: "custom" });
  }

  return (
    <div className="p-trp" role="group" aria-label="Time range">
      {presets.map((p) => (
        <Button
          key={p}
          size="sm"
          aria-pressed={value.preset === p}
          onClick={() => applyPreset(p)}
          variant={value.preset === p ? "primary" : "secondary"}
        >
          {p}
        </Button>
      ))}
      <Button size="sm" aria-pressed={customOpen} onClick={() => setCustomOpen((o) => !o)}>
        custom
      </Button>
      {customOpen ? (
        <span className="p-trp-custom">
          <Input label="From (UTC)" value={from} onChange={setFrom} mono error={error} />
          <Input label="To (UTC)" value={to} onChange={setTo} mono />
          <Button size="sm" variant="primary" onClick={applyCustom}>
            Apply
          </Button>
        </span>
      ) : null}
    </div>
  );
}
