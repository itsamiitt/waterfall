import { useId, type InputHTMLAttributes } from "react";

export interface InputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "onChange" | "value"> {
  label: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  description?: string;
  required?: boolean;
  /** Monospace rendering for secrets / ids (doc 08 §6.2). */
  mono?: boolean;
}

/** Label always rendered; error and description wired via aria-describedby (doc 08 §9). */
export function Input({
  label,
  value,
  onChange,
  error,
  description,
  required,
  mono,
  id,
  ...rest
}: InputProps) {
  const autoId = useId();
  const inputId = id ?? autoId;
  const descId = description ? `${inputId}-desc` : undefined;
  const errId = error ? `${inputId}-err` : undefined;
  const describedBy = [descId, errId].filter(Boolean).join(" ") || undefined;

  return (
    <div className="p-field">
      <label className="p-field-label" htmlFor={inputId}>
        {label}
        {required ? (
          <span className="p-field-required" aria-hidden="true">
            {" *"}
          </span>
        ) : null}
      </label>
      {description ? (
        <span className="p-field-description" id={descId}>
          {description}
        </span>
      ) : null}
      <input
        {...rest}
        id={inputId}
        className="p-input"
        data-mono={mono || undefined}
        value={value}
        required={required}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy}
        onChange={(e) => onChange(e.currentTarget.value)}
      />
      {error ? (
        <span className="p-field-error" id={errId} role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}
