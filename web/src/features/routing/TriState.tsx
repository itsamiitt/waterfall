// features/routing/TriState.tsx — the per-Provider tri-state override control (doc 07 §2,
// doc 09 §6.1). It edits the LOCAL override mode (inherit/off/on) and, beside it, renders the
// RESOLVED effective value + its source scope straight from the resolver output — never
// client-derived (doc 07 §3.2). Pure presentational: no router/query hooks, so it render-tests
// under react-dom/server (the P10 "tri-state resolver display" gate).
import { Badge } from "../../design/primitives";
import { describeEffective, effectiveToken, triLabel } from "./lifecycle";
import type { EffectiveOverride, TriMode } from "./types";

const MODES: TriMode[] = ["inherit", "off", "on"];

export interface TriStateControlProps {
  provider: string;
  mode: TriMode;
  onChange: (mode: TriMode) => void;
  /** Resolver output for this Provider (effective value + source scope); may be absent. */
  effective?: EffectiveOverride;
  disabled?: boolean;
}

export function TriStateControl({ provider, mode, onChange, effective, disabled }: TriStateControlProps) {
  return (
    <div className="rt-tristate">
      <div
        className="rt-tristate-seg"
        role="radiogroup"
        aria-label={`${provider} override mode`}
      >
        {MODES.map((m) => (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={mode === m}
            data-active={mode === m || undefined}
            disabled={disabled}
            className="rt-tristate-opt"
            onClick={() => onChange(m)}
          >
            {triLabel(m)}
          </button>
        ))}
      </div>
      {effective ? (
        <span className="rt-effective" title={describeEffective(effective)}>
          <Badge
            status={effectiveToken(effective)}
            label={describeEffective(effective)}
            icon="flag"
            family="outlined"
          />
        </span>
      ) : (
        <span className="rt-effective rt-effective-none">no resolved value yet</span>
      )}
    </div>
  );
}
