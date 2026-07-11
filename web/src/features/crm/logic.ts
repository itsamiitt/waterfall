// features/crm/logic.ts — pure helpers for the CRM connections surface (unit-tested without hooks).

const KNOWN: Record<string, string> = {
  salesforce: "Salesforce",
  hubspot: "HubSpot",
  pipedrive: "Pipedrive",
  dynamics: "Dynamics 365",
};

/** A display label for a CRM provider slug: a known brand name, else the slug title-cased. */
export function providerLabel(slug: string): string {
  if (slug === "") return "—";
  return KNOWN[slug] ?? slug.charAt(0).toUpperCase() + slug.slice(1);
}
