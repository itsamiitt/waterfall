// /mfa (doc 08 §3, §7; doc 12 P8 "enroll + verify + recovery code"):
//   - verify: TOTP code or one-time recovery code completes login (POST /auth/mfa/verify)
//   - enroll (?enroll=1, authenticated sessions only): POST /auth/mfa/enroll returns the
//     otpauth URI once -> rendered as a QR via `qrcode` (dynamically imported so the
//     library stays out of the initial bundle); confirm returns recovery codes once.
import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { isApiError } from "../../api/client";
import { Button, CodeBlock, Input } from "../../design/primitives";
import { useMfaConfirm, useMfaEnroll, useMfaVerify } from "./api";

function safeNext(raw: string | null): string {
  return raw && raw.startsWith("/") && !raw.startsWith("//") ? raw : "/";
}

function VerifyForm() {
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const verify = useMfaVerify();

  function submit(e: FormEvent) {
    e.preventDefault();
    verify.mutate(
      { code: code.trim() },
      {
        onSuccess: (res) => {
          if (res.status === "ok") void navigate(safeNext(params.get("next")), { replace: true });
        },
      },
    );
  }

  return (
    <form className="auth-card" onSubmit={submit}>
      <h1>Two-factor verification</h1>
      <Input
        label={recovery ? "Recovery code" : "Authenticator code"}
        value={code}
        onChange={setCode}
        required
        mono
        autoComplete="one-time-code"
        inputMode={recovery ? "text" : "numeric"}
        autoFocus
        description={
          recovery
            ? "Each recovery code works exactly once."
            : "6-digit code from your authenticator app."
        }
      />
      {verify.isError ? (
        <p className="form-error" role="alert">
          {isApiError(verify.error) ? verify.error.message : "verification failed"}
        </p>
      ) : null}
      <Button type="submit" variant="primary" loading={verify.isPending}>
        Verify
      </Button>
      <Button variant="ghost" onClick={() => setRecovery((r) => !r)}>
        {recovery ? "Use authenticator code" : "Use a recovery code"}
      </Button>
    </form>
  );
}

function EnrollFlow() {
  const enroll = useMfaEnroll();
  const confirm = useMfaConfirm();
  const [qrDataUrl, setQrDataUrl] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [params] = useSearchParams();
  const navigate = useNavigate();

  const otpauthUrl = enroll.data?.otpauth_url;

  useEffect(() => {
    if (!otpauthUrl) return;
    let cancelled = false;
    // The seed is displayed once and never re-fetched (doc 08 §7); QR rendering is fully
    // client-side. Dynamic import keeps `qrcode` out of the initial chunk (doc 08 §10).
    void import("qrcode").then(async (QRCode) => {
      const url = await QRCode.toDataURL(otpauthUrl, { margin: 1, width: 192 });
      if (!cancelled) setQrDataUrl(url);
    });
    return () => {
      cancelled = true;
    };
  }, [otpauthUrl]);

  if (confirm.isSuccess) {
    return (
      <div className="auth-card">
        <h1>Recovery codes</h1>
        <p>
          Store these somewhere safe. <strong>You will not see them again.</strong>
        </p>
        <CodeBlock code={confirm.data.recovery_codes.join("\n")} copyable />
        <Button
          variant="primary"
          onClick={() => void navigate(safeNext(params.get("next")), { replace: true })}
        >
          I have saved my recovery codes
        </Button>
      </div>
    );
  }

  if (!enroll.isSuccess) {
    return (
      <div className="auth-card">
        <h1>Enroll two-factor authentication</h1>
        <p>Generates a TOTP secret and shows it as a QR code exactly once.</p>
        {enroll.isError ? (
          <p className="form-error" role="alert">
            {isApiError(enroll.error) ? enroll.error.message : "enrollment failed"}
          </p>
        ) : null}
        <Button variant="primary" loading={enroll.isPending} onClick={() => enroll.mutate()}>
          Begin enrollment
        </Button>
      </div>
    );
  }

  return (
    <form
      className="auth-card"
      onSubmit={(e) => {
        e.preventDefault();
        confirm.mutate({ code: code.trim() });
      }}
    >
      <h1>Scan and confirm</h1>
      {qrDataUrl ? (
        <img className="mfa-qr" src={qrDataUrl} alt="TOTP enrollment QR code" width={192} height={192} />
      ) : (
        <div className="skeleton" style={{ height: 192, width: 192, margin: "0 auto" }} />
      )}
      <CodeBlock code={otpauthUrl ?? ""} copyable />
      <Input
        label="First authenticator code"
        value={code}
        onChange={setCode}
        required
        mono
        autoComplete="one-time-code"
        inputMode="numeric"
      />
      {confirm.isError ? (
        <p className="form-error" role="alert">
          {isApiError(confirm.error) ? confirm.error.message : "confirmation failed"}
        </p>
      ) : null}
      <Button type="submit" variant="primary" loading={confirm.isPending}>
        Confirm enrollment
      </Button>
    </form>
  );
}

export default function MfaPage() {
  const [params] = useSearchParams();
  return <main>{params.get("enroll") ? <EnrollFlow /> : <VerifyForm />}</main>;
}
