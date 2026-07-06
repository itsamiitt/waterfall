// features/security/provisioning — DTOs for the operator Tenant-provisioning path (doc 15 §T1,
// ADR-0021). Field names are snake_case to match the wire (internal/dash/provisioning/http.go).

/** POST /tenants request body. `id` is the Tenant slug (^[a-z0-9-]{1,64}$); `plan_tier` is
 * optional ("" → NULL server-side). */
export interface ProvisionRequest {
  id: string;
  name: string;
  plan_tier: string;
  admin_email: string;
}

/** POST /tenants 201 response — the invite token is returned exactly once. */
export interface ProvisionResult {
  tenant_id: string;
  invite_token: string;
}

/** POST /auth/accept-invite request body (public / pre-session). */
export interface AcceptInviteRequest {
  token: string;
  password: string;
}

/** Plan tiers offered in the provisioning form. The server validates; this only shapes the
 * select. Kept small and closed (no free text) per the Select primitive contract. */
export const PLAN_TIERS = ["free", "starter", "growth", "enterprise"] as const;
export type PlanTier = (typeof PLAN_TIERS)[number];
