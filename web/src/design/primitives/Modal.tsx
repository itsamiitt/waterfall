import { useRef, type ReactNode, type RefObject } from "react";
import { useFocusTrap } from "./focus";
import { Button } from "./Button";
import { Icon } from "./Icon";

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  size?: "md" | "lg";
  /** Defaults to the first non-destructive control (focus.ts picks the first focusable). */
  initialFocusRef?: RefObject<HTMLElement | null>;
  /** Escape/overlay close is suppressed while a mutation is in flight (doc 08 §6.2). */
  busy?: boolean;
  children: ReactNode;
  footer?: ReactNode;
}

export function Modal({
  open,
  onClose,
  title,
  size = "md",
  initialFocusRef,
  busy = false,
  children,
  footer,
}: ModalProps) {
  const ref = useRef<HTMLDivElement>(null);
  const close = busy ? () => {} : onClose;
  useFocusTrap(ref, open, close, initialFocusRef);
  if (!open) return null;

  return (
    <div className="p-overlay" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div
        ref={ref}
        className="p-modal"
        data-size={size}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="p-modal-header">
          <h2 className="p-modal-title">{title}</h2>
          <Button variant="ghost" size="sm" onClick={close} aria-label="Close" disabled={busy}>
            <Icon name="x" />
          </Button>
        </div>
        <div className="p-modal-body">{children}</div>
        {footer ? <div className="p-modal-footer">{footer}</div> : null}
      </div>
    </div>
  );
}
