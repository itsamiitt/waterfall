// features/security/provisioning/ProvisioningPage.tsx — operator-only Tenant provisioning form
// (doc 15 §T1, ADR-0021). id (slug) + name + plan_tier + admin_email + MFA code → POST /tenants.
// The returned `invite_token` is shown EXACTLY ONCE (copyable, with the ready-made accept-invite
// link); it is never refetched, mirroring the MFA-seed / recovery-code "shown once" surfaces.
import { useState } from "react";
import { isApiError } from "../../../api/client";
import { toast } from "../../../app/toast";
import { Button, CodeBlock, EmptyState, Input, Select, type SelectOption } from "../../../design/primitives";
import { useProvisionTenant } from "./api";
import { PLAN_TIERS, type ProvisionResult } from "./types";

const PLAN_OPTS: SelectOption[] = PLAN_TIERS.map((t) => ({ value: t, label: t }));

export default function ProvisioningPage() {
  const provision = useProvisionTenant();
  const [f, setF] = useState({ id: "", name: "", plan_tier: "", admin_email: "", mfa: "" });
  const set = (k: keyof typeof f) => (v: string) => setF((s) => ({ ...s, [k]: v }));
  const [result, setResult] = useState<ProvisionResult | null>(null);

  const inviteLink = result
    ? `${location.origin}/accept-invite?token=${encodeURIComponent(result.invite_token)}`
    : "";

  function submit() {
    provision.mutate(
      {
        body: {
          id: f.id.trim(),
          name: f.name.trim(),
          plan_tier: f.plan_tier,
          admin_email: f.admin_email.trim(),
        },
        mfaCode: f.mfa || undefined,
      },
      {
        onSuccess: (r) => {
          setResult(r);
          toast.success(`Tenant ${r.tenant_id} provisioned — copy the invite now`);
        },
      },
    );
  }

  if (result) {
    return (
      <>
        <div className="page-header">
          <h1 tabIndex={-1}>Provision Tenant</h1>
          <span className="page-header-meta">Tenant {result.tenant_id} created</span>
        </div>
        <div className="section" style={{ maxWidth: 640 }}>
          <p>
            The first admin sets their password via the one-time invite below.{" "}
            <strong>You will not see this token again</strong> — copy it now (it is not stored in
            plaintext anywhere).
          </p>
          <div className="section-title">Invite link</div>
          <CodeBlock code={inviteLink} copyable />
          <div className="section-title">Invite token</div>
          <CodeBlock code={result.invite_token} copyable />
          <div className="action-bar">
            <Button
              variant="primary"
              onClick={() => {
                setResult(null);
                setF({ id: "", name: "", plan_tier: "", admin_email: "", mfa: "" });
              }}
            >
              Provision another Tenant
            </Button>
          </div>
        </div>
      </>
    );
  }

  const stepUpNeeded =
    provision.isError && isApiError(provision.error) && provision.error.code === "mfa_required";
  const errText = provision.isError
    ? isApiError(provision.error)
      ? provision.error.message
      : "provisioning failed"
    : undefined;

  return (
    <>
      <div className="page-header">
        <h1 tabIndex={-1}>Provision Tenant</h1>
        <span className="page-header-meta">Operator-only · audited · MFA step-up</span>
      </div>
      <form
        className="section"
        style={{ maxWidth: 520 }}
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Input
          label="Tenant id (slug)"
          value={f.id}
          onChange={set("id")}
          mono
          required
          description="Lowercase letters, digits, hyphens (^[a-z0-9-]{1,64}$). Immutable once created."
        />
        <Input label="Display name" value={f.name} onChange={set("name")} required />
        <Select
          label="Plan tier"
          options={PLAN_OPTS}
          value={f.plan_tier}
          placeholder="select plan tier"
          onChange={set("plan_tier")}
        />
        <Input
          label="First admin email"
          type="email"
          value={f.admin_email}
          onChange={set("admin_email")}
          required
          autoComplete="off"
          description="Becomes the Tenant's first tenant_admin (status invited, no password yet)."
        />
        <Input
          label="MFA code"
          value={f.mfa}
          onChange={set("mfa")}
          mono
          inputMode="numeric"
          autoComplete="one-time-code"
          description="Required — creating a Tenant is a high-authority operator action (doc 05 §5.4)."
        />
        {stepUpNeeded ? (
          <p className="p-field-error" role="alert">
            Enter a valid MFA code to continue.
          </p>
        ) : errText ? (
          <p className="form-error" role="alert">
            {errText}
          </p>
        ) : null}
        <div className="action-bar">
          <Button
            type="submit"
            variant="primary"
            loading={provision.isPending}
            disabled={!f.id || !f.name || !f.admin_email || !f.mfa}
          >
            Provision Tenant
          </Button>
        </div>
      </form>
    </>
  );
}

/** Rendered by the security route boundary when a non-operator reaches /security/provisioning.
 * Mirrors RequireRole's forbidden empty state (the server enforces independently). */
export function ProvisioningForbidden() {
  return (
    <EmptyState
      variant="error"
      title="Tenant provisioning is operator-only"
      errorCode="forbidden"
      body="Only platform operators can provision Tenants. The server enforces this independently."
    />
  );
}
