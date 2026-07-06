// features/security/MfaPolicyPanel.tsx — the per-Tenant require_mfa toggle (doc 15 §T2 / SEC-5).
// tenant_admin only. Flipping the switch opens an MFA step-up dialog (X-MFA-Code, §5.4, like keys
// rotate/create); the write is audited server-side. On success the switch reflects the
// authoritative response. A 401 mfa_required keeps the dialog open with an inline re-prompt.
import { useEffect, useState } from "react";
import { isApiError } from "../../api/client";
import { toast } from "../../app/toast";
import { Button, Input, Modal } from "../../design/primitives";
import { useMfaPolicy, useSetMfaPolicy } from "./api";

export function MfaPolicyPanel() {
  const policy = useMfaPolicy();
  const setPolicy = useSetMfaPolicy();
  const [pending, setPending] = useState<boolean | null>(null); // the requested new value while confirming
  const [code, setCode] = useState("");

  useEffect(() => {
    if (pending === null) setCode("");
  }, [pending]);

  const current = policy.data?.require_mfa ?? false;

  function confirm() {
    if (pending === null) return;
    setPolicy.mutate(
      { require_mfa: pending, mfaCode: code.trim() || undefined },
      {
        onSuccess: (data) => {
          toast.success(data.require_mfa ? "MFA is now required for all users" : "MFA requirement disabled");
          setPending(null);
        },
      },
    );
  }

  const stepUpError =
    setPolicy.isError && isApiError(setPolicy.error) && setPolicy.error.code === "mfa_required"
      ? "Enter a valid MFA code to apply this change."
      : setPolicy.isError
        ? isApiError(setPolicy.error)
          ? setPolicy.error.message
          : "Could not update the policy"
        : undefined;

  return (
    <section className="section" style={{ maxWidth: 520 }} aria-label="MFA policy">
      <div className="section-title">Multi-factor authentication policy</div>
      {policy.isError ? (
        <p className="p-field-error" role="alert">
          Could not load the MFA policy
          {isApiError(policy.error) ? ` (${policy.error.code})` : ""}.{" "}
          <button className="link-btn" onClick={() => void policy.refetch()}>
            Retry
          </button>
        </p>
      ) : policy.isPending ? (
        <div className="skeleton" style={{ height: 64 }} aria-busy="true" aria-label="Loading MFA policy" />
      ) : (
        <>
          <p style={{ color: "var(--color-text-muted)", fontSize: "var(--text-sm)" }}>
            When enabled, every user in your Tenant must enroll an authenticator. A user without one
            is routed into enrollment at their next sign-in and cannot reach the dashboard until
            enrolled and verified.
          </p>
          <label className="radio-chip">
            <input
              type="checkbox"
              role="switch"
              checked={current}
              aria-checked={current}
              disabled={setPolicy.isPending}
              onChange={(e) => setPending(e.currentTarget.checked)}
            />
            Require MFA for all users {current ? "(on)" : "(off)"}
          </label>
        </>
      )}

      <Modal
        open={pending !== null}
        onClose={() => setPending(null)}
        title={pending ? "Require MFA — step-up" : "Disable MFA requirement — step-up"}
        busy={setPolicy.isPending}
        footer={
          <>
            <Button onClick={() => setPending(null)} disabled={setPolicy.isPending}>
              Cancel
            </Button>
            <Button
              variant={pending ? "primary" : "danger"}
              onClick={confirm}
              loading={setPolicy.isPending}
              disabled={!code.trim()}
            >
              {pending ? "Require MFA" : "Disable requirement"}
            </Button>
          </>
        }
      >
        <p>
          {pending
            ? "All users will be required to enroll an authenticator before they can use the dashboard."
            : "Users will no longer be forced to enroll MFA. Existing enrollments are unaffected."}
        </p>
        <Input
          label="Enter TOTP code (X-MFA-Code)"
          value={code}
          onChange={setCode}
          inputMode="numeric"
          autoComplete="one-time-code"
          mono
          required
        />
        {stepUpError ? (
          <p role="alert" style={{ color: "var(--status-error)" }}>
            {stepUpError}
          </p>
        ) : null}
      </Modal>
    </section>
  );
}
