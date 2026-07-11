// features/crm/types.ts — CRM outbound connections read model (docs/research-intelligence/08, ADR-0030).
// A tenant-scoped read of which CRMs a Tenant pushes to and their status. Credential material
// (secret_ref / config) is NEVER projected by the backend — it is not part of this shape.

/** One configured CRM connection (list row). Mirrors dash/crm ConnectionSummary. */
export interface ConnectionSummary {
  connection_id: string;
  provider: string;
  status: string;
  created_at: string;
  updated_at: string;
}

/** GET /crm/connections envelope. */
export interface ConnectionsResponse {
  items: ConnectionSummary[];
}
