import type { ButtonHTMLAttributes, ReactNode } from "react";

export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className"> {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  size?: "sm" | "md";
  loading?: boolean;
  iconStart?: ReactNode;
  children?: ReactNode;
}

/** doc 08 §6.2: `loading` disables + spinner; `danger` is reserved for destructive intents. */
export function Button({
  variant = "secondary",
  size = "md",
  loading = false,
  iconStart,
  disabled,
  children,
  type,
  ...rest
}: ButtonProps) {
  return (
    <button
      {...rest}
      type={type ?? "button"}
      className="p-btn"
      data-variant={variant}
      data-size={size}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
    >
      {loading ? <span className="p-btn-spinner" aria-hidden="true" /> : iconStart}
      {children}
    </button>
  );
}
