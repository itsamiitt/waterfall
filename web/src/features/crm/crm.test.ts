// features/crm unit tests: the provider label maps known CRM slugs to brand names and title-cases the rest
// (never renders a raw empty slug).
import { describe, expect, it } from "vitest";
import { providerLabel } from "./logic";

describe("crm providerLabel", () => {
  it("maps known CRM slugs to brand names", () => {
    expect(providerLabel("salesforce")).toBe("Salesforce");
    expect(providerLabel("hubspot")).toBe("HubSpot");
  });

  it("title-cases unknown slugs and renders empty as a dash", () => {
    expect(providerLabel("zoho")).toBe("Zoho");
    expect(providerLabel("")).toBe("—");
  });
});
