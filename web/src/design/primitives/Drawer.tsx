import { useRef, type ReactNode } from "react";
import { useFocusTrap } from "./focus";
import { Button } from "./Button";
import { Icon } from "./Icon";

export interface DrawerProps {
  open: boolean;
  onClose: () => void;
  title: string;
  side?: "right";
  width?: number;
  children: ReactNode;
}

/** Same focus contract as Modal (doc 08 §6.2); hosts detail/inspection panels. */
export function Drawer({ open, onClose, title, width, children }: DrawerProps) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref, open, onClose);
  if (!open) return null;

  return (
    <div
      className="p-overlay p-drawer-overlay"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        ref={ref}
        className="p-modal p-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={width ? { width } : undefined}
      >
        <div className="p-modal-header">
          <h2 className="p-modal-title">{title}</h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
            <Icon name="x" />
          </Button>
        </div>
        <div className="p-modal-body">{children}</div>
      </div>
    </div>
  );
}
