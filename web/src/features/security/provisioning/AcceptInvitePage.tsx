// /accept-invite (public / pre-session, doc 15 §T1, ADR-0021): the invite token IS the
// credential. Reads the token from ?token= (prefilled, editable) plus a new password →
// POST /auth/accept-invite → on success routes to /login. Styled like LoginPage/MfaPage
// (`.auth-card`) since it renders outside the authenticated app shell.
import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { isApiError } from "../../../api/client";
import { toast } from "../../../app/toast";
import { Button, Input } from "../../../design/primitives";
import { useAcceptInvite } from "./api";

export default function AcceptInvitePage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const accept = useAcceptInvite();
  const [token, setToken] = useState(params.get("token") ?? "");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");

  const mismatch = confirm.length > 0 && password !== confirm;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch) return;
    accept.mutate(
      { token: token.trim(), password },
      {
        onSuccess: () => {
          toast.success("Password set — sign in to continue");
          void navigate("/login", { replace: true });
        },
      },
    );
  }

  const errText = accept.isError
    ? isApiError(accept.error)
      ? accept.error.message
      : "could not accept invite"
    : undefined;

  return (
    <main>
      <form className="auth-card" onSubmit={submit}>
        <h1>Set your password</h1>
        <p>
          Complete your invitation by choosing a password. Your invite token is single-use and
          expires; it never leaves this device in plaintext.
        </p>
        <Input
          label="Invite token"
          value={token}
          onChange={setToken}
          required
          mono
          autoComplete="off"
          description="Prefilled from your invite link; paste it here if the link did not carry it."
        />
        <Input
          label="New password"
          type="password"
          value={password}
          onChange={setPassword}
          required
          autoComplete="new-password"
          description="At least 8 characters."
        />
        <Input
          label="Confirm password"
          type="password"
          value={confirm}
          onChange={setConfirm}
          required
          autoComplete="new-password"
          error={mismatch ? "Passwords do not match." : undefined}
        />
        {errText ? (
          <p className="form-error" role="alert">
            {errText}
          </p>
        ) : null}
        <Button
          type="submit"
          variant="primary"
          loading={accept.isPending}
          disabled={!token.trim() || password.length < 8 || mismatch}
        >
          Set password
        </Button>
      </form>
    </main>
  );
}
