// Approvals unit tests (P11 gate): the decision travels with the TOTP code in the X-MFA-Code
// HEADER (never the body), and the step-up dialog collects both a code and a required comment
// (doc 04 §2.12, doc 09 §11.1).
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { buildDecisionRequest } from "./api";
import { StepUpModal } from "./StepUpModal";

describe("approval decision step-up (doc 04 §2.12)", () => {
  it("sends the TOTP code as the X-MFA-Code header, not in the body", () => {
    const req = buildDecisionRequest("looks compromised", "738201");
    expect(req.headers["X-MFA-Code"]).toBe("738201");
    expect(req.body).toEqual({ comment: "looks compromised" });
    expect(JSON.stringify(req.body)).not.toContain("738201");
  });

  it("the step-up dialog collects a TOTP code and a required comment", () => {
    const html = renderToStaticMarkup(
      createElement(StepUpModal, {
        open: true,
        action: "Approve",
        onClose: () => {},
        onSubmit: () => {},
      }),
    );
    expect(html).toContain("X-MFA-Code");
    expect(html).toContain("Comment");
    expect(html).toContain("Confirm Approve");
    expect(html).toContain('role="dialog"');
  });

  it("surfaces a decision error verbatim (e.g. four-eyes) and stays open", () => {
    const html = renderToStaticMarkup(
      createElement(StepUpModal, {
        open: true,
        action: "Approve",
        error: "four-eyes: requester cannot approve own request",
        onClose: () => {},
        onSubmit: () => {},
      }),
    );
    expect(html).toContain("four-eyes: requester cannot approve own request");
    expect(html).toContain('role="alert"');
  });
});
