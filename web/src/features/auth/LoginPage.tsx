// /login (doc 08 §3, §7): email + password -> session cookie; status:"mfa_required" routes
// to /mfa; "ok" lands on ?next= or the overview.
import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { isApiError } from "../../api/client";
import { Button, Input } from "../../design/primitives";
import { useLogin } from "./api";

function safeNext(raw: string | null): string {
  // Only same-app absolute paths — never external redirect targets.
  return raw && raw.startsWith("/") && !raw.startsWith("//") ? raw : "/";
}

export default function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const login = useLogin();

  function submit(e: FormEvent) {
    e.preventDefault();
    login.mutate(
      { email, password },
      {
        onSuccess: (res) => {
          const next = safeNext(params.get("next"));
          if (res.status === "mfa_required") {
            void navigate(`/mfa?next=${encodeURIComponent(next)}`);
          } else if (res.status === "mfa_enrollment_required") {
            // Tenant require_mfa policy is on and this user has no authenticator yet (doc 15 §T2):
            // route into enrollment. The app shell stays closed until enrolled + verified — we do
            // NOT navigate to `next` here; EnrollFlow lands there only after confirmation.
            void navigate(`/mfa?enroll=1&next=${encodeURIComponent(next)}`);
          } else {
            void navigate(next, { replace: true });
          }
        },
      },
    );
  }

  const errText = login.isError
    ? isApiError(login.error)
      ? login.error.message
      : "login failed"
    : undefined;

  return (
    <main>
      <form className="auth-card" onSubmit={submit}>
        <h1>Sign in</h1>
        <Input
          label="Email"
          type="email"
          value={email}
          onChange={setEmail}
          required
          autoComplete="username"
          autoFocus
        />
        <Input
          label="Password"
          type="password"
          value={password}
          onChange={setPassword}
          required
          autoComplete="current-password"
        />
        {errText ? (
          <p className="form-error" role="alert">
            {errText}
          </p>
        ) : null}
        <Button type="submit" variant="primary" loading={login.isPending}>
          Sign in
        </Button>
      </form>
    </main>
  );
}
