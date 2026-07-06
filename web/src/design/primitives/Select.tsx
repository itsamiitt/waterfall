import { useId, type ReactNode } from "react";

export interface SelectOption<V extends string = string> {
  value: V;
  label: string;
  icon?: ReactNode;
}

export interface SelectProps<V extends string = string> {
  label: string;
  options: readonly SelectOption<V>[];
  value: V | "";
  onChange: (value: V) => void;
  placeholder?: string;
  disabled?: boolean;
  error?: string;
  id?: string;
}

/** Options are typed to closed vocabularies (GET /v1/admin/meta/enums) — no free text. */
export function Select<V extends string = string>({
  label,
  options,
  value,
  onChange,
  placeholder,
  disabled,
  error,
  id,
}: SelectProps<V>) {
  const autoId = useId();
  const selectId = id ?? autoId;
  const errId = error ? `${selectId}-err` : undefined;
  return (
    <div className="p-field">
      <label className="p-field-label" htmlFor={selectId}>
        {label}
      </label>
      <select
        id={selectId}
        className="p-select"
        value={value}
        disabled={disabled}
        aria-invalid={error ? true : undefined}
        aria-describedby={errId}
        onChange={(e) => onChange(e.currentTarget.value as V)}
      >
        {placeholder !== undefined ? (
          <option value="" disabled>
            {placeholder}
          </option>
        ) : null}
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {error ? (
        <span className="p-field-error" id={errId} role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}
