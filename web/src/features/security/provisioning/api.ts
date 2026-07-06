// features/security/provisioning/api.ts — the ONLY place the operator Tenant-provisioning
// endpoint paths are named (doc 08 §2; doc 15 §T1, ADR-0021). Screen → endpoint map:
//   ProvisioningPage    POST /tenants            (operator-only; X-MFA-Code step-up + Idempotency-Key)
//   AcceptInvitePage    POST /auth/accept-invite (PUBLIC / pre-session; token is the credential)
import { useMutation } from "@tanstack/react-query";
import { post } from "../../../api/client";
import type {
  AcceptInviteRequest,
  ProvisionRequest,
  ProvisionResult,
} from "./types";

/** POST /tenants — create a customer Tenant + first tenant_admin + one-time invite token.
 * Mirrors keys create/import step-up: the TOTP code rides the X-MFA-Code header (§5.4), never
 * the body; the Idempotency-Key header is added automatically by the client (doc 04 §1.3). The
 * `invite_token` is returned exactly once, so it is surfaced (copyable) on success and never
 * refetched. */
export function useProvisionTenant() {
  return useMutation({
    mutationFn: (vars: { body: ProvisionRequest; mfaCode?: string }) =>
      post<ProvisionResult>(
        "/tenants",
        vars.body,
        vars.mfaCode ? { headers: { "X-MFA-Code": vars.mfaCode } } : undefined,
      ),
  });
}

/** POST /auth/accept-invite — public, token-authenticated: sets the first admin's password.
 * No session/CSRF exists yet, so the client sends neither; the endpoint is Idempotency-exempt. */
export function useAcceptInvite() {
  return useMutation({
    mutationFn: (body: AcceptInviteRequest) => post<{ status: string }>("/auth/accept-invite", body),
  });
}
