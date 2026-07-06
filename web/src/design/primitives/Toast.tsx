import { Link } from "react-router";
import { Button } from "./Button";
import { Icon, type IconName } from "./Icon";

export type ToastKind = "success" | "error" | "info";

export interface ToastItem {
  id: string;
  kind: ToastKind;
  message: string;
  /** Deep link (job results, approvals — doc 08 §6.2). */
  action?: { label: string; href: string };
}

const KIND_ICON: Record<ToastKind, IconName> = {
  success: "check",
  error: "triangle",
  info: "dot",
};

export function Toast({ item, onDismiss }: { item: ToastItem; onDismiss: (id: string) => void }) {
  return (
    <div className="p-toast" data-kind={item.kind}>
      <Icon name={KIND_ICON[item.kind]} />
      <span>{item.message}</span>
      {item.action ? <Link to={item.action.href}>{item.action.label}</Link> : null}
      <span className="p-toast-dismiss">
        <Button variant="ghost" size="sm" aria-label="Dismiss" onClick={() => onDismiss(item.id)}>
          <Icon name="x" />
        </Button>
      </span>
    </div>
  );
}

/** Rendered once by app/providers.tsx as a polite live region (doc 08 §9: announce once;
 * tick updates never announce). */
export function ToastRegion({
  items,
  onDismiss,
}: {
  items: readonly ToastItem[];
  onDismiss: (id: string) => void;
}) {
  return (
    <div className="p-toast-region" role="status" aria-live="polite" aria-atomic="false">
      {items.map((t) => (
        <Toast key={t.id} item={t} onDismiss={onDismiss} />
      ))}
    </div>
  );
}
