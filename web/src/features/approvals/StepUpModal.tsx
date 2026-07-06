// features/approvals/StepUpModal.tsx — the MFA STEP-UP dialog (doc 09 §11.1, doc 04 §2.12).
// Collects the TOTP code (sent as the X-MFA-Code header, never in the body) and a REQUIRED
// comment. A 401 mfa_required re-prompts in-dialog; a 403 forbidden (self-approval) renders the
// four-eyes message; both arrive via the `error` prop and keep the dialog open.
import { useEffect, useState } from "react";
import { Button, Input, Modal } from "../../design/primitives";

export interface StepUpModalProps {
  open: boolean;
  /** "Approve" | "Reject" — labels the confirm button and the dialog. */
  action: "Approve" | "Reject";
  busy?: boolean;
  error?: string;
  onClose: () => void;
  onSubmit: (code: string, comment: string) => void;
}

export function StepUpModal({ open, action, busy, error, onClose, onSubmit }: StepUpModalProps) {
  const [code, setCode] = useState("");
  const [comment, setComment] = useState("");

  useEffect(() => {
    if (open) {
      setCode("");
      setComment("");
    }
  }, [open]);

  const blocked = !code.trim() || !comment.trim();

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`${action} — MFA step-up`}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant={action === "Reject" ? "danger" : "primary"}
            onClick={() => onSubmit(code.trim(), comment.trim())}
            loading={busy}
            disabled={blocked}
          >
            Confirm {action}
          </Button>
        </>
      }
    >
      <Input
        label="Enter TOTP code (X-MFA-Code)"
        value={code}
        onChange={setCode}
        inputMode="numeric"
        autoComplete="one-time-code"
        mono
        required
      />
      <Input
        label="Comment"
        value={comment}
        onChange={setComment}
        required
        description="A comment is required and recorded with the decision (four-eyes audit)."
      />
      {error ? (
        <p role="alert" style={{ color: "var(--status-error)" }}>
          {error}
        </p>
      ) : null}
    </Modal>
  );
}
