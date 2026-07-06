import type { ReactNode } from "react";
import { Link } from "react-router";
import { Button } from "./Button";
import { Icon } from "./Icon";

export interface EmptyStateProps {
  /** The three-state system of doc 08 §8: zero-data is onboarding, zero-results says
   * "no rows match the current filters", error shows the envelope code + Retry. */
  variant: "zero-data" | "zero-results" | "error";
  title: string;
  body?: ReactNode;
  /** Uniform envelope `error.code` (doc 04 §1.6), shown verbatim on the error variant. */
  errorCode?: string;
  action?: { label: string; onClick?: () => void; href?: string };
}

export function EmptyState({ variant, title, body, errorCode, action }: EmptyStateProps) {
  return (
    <div className="p-empty" data-variant={variant} role={variant === "error" ? "alert" : undefined}>
      <Icon name={variant === "error" ? "triangle" : variant === "zero-results" ? "slash" : "flag"} />
      <h2 className="p-empty-title">{title}</h2>
      {errorCode ? <code className="p-empty-code">{errorCode}</code> : null}
      {body ? <div>{body}</div> : null}
      {action ? (
        action.href ? (
          <Link to={action.href}>
            <Button variant="primary">{action.label}</Button>
          </Link>
        ) : (
          <Button variant="primary" onClick={action.onClick}>
            {action.label}
          </Button>
        )
      ) : null}
    </div>
  );
}
