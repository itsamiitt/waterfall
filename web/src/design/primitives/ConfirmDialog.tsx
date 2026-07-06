import { useState } from "react";
import { Modal } from "./Modal";
import { Button } from "./Button";
import { Input } from "./Input";

export interface ConfirmDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  body?: string;
  /** Destructive ops list concrete consequences (doc 08 §6.2, e.g. "last used 2h ago —
   * 14,203 calls this month"). */
  consequences?: readonly string[];
  confirmLabel: string;
  danger?: boolean;
  /** When set, the confirm button stays disabled until the phrase is typed exactly. */
  requireTypedPhrase?: string;
  busy?: boolean;
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  body,
  consequences,
  confirmLabel,
  danger = false,
  requireTypedPhrase,
  busy = false,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState("");
  const blocked = requireTypedPhrase !== undefined && typed !== requireTypedPhrase;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant={danger ? "danger" : "primary"}
            onClick={onConfirm}
            disabled={blocked}
            loading={busy}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {body ? <p>{body}</p> : null}
      {consequences && consequences.length > 0 ? (
        <ul className="p-consequences">
          {consequences.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      ) : null}
      {requireTypedPhrase !== undefined ? (
        <Input
          label={`Type "${requireTypedPhrase}" to confirm`}
          value={typed}
          onChange={setTyped}
          mono
          autoComplete="off"
        />
      ) : null}
    </Modal>
  );
}
